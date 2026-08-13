package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

// cmdUpdate is the CI entry point. It owns the complete transaction: clone,
// refresh every active chart, commit, push, and report the aggregate status.
// Successes are published even when other entries fail; the run exits non-zero
// only for transient errors, while permanent-shape failures count toward
// auto-pause instead of staying loud forever.
func cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	inst := fs.String("instance", "", "instance repo (owner/name)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	c, err := ghapi.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)

		return 1
	}

	target := *inst
	if target == "" {
		if target, err = defaultInstance(c, ""); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)

			return 1
		}
	}

	var transientFailures []string

	err = instance.Transact(target, c.Token(), func(r *instance.Repo) error {
		transientFailures = nil

		mPath := filepath.Join(r.Dir, manifest.FileName)

		m, err := manifest.Load(mPath)
		if err != nil {
			return err
		}

		succeeded := 0

		for i := range m.Charts {
			e := &m.Charts[i]
			if e.State != manifest.StateActive {
				continue
			}

			if err := refreshOne(c, r, e); err != nil {
				var se *ghapi.StatusError

				if errors.As(err, &se) && se.IsPermanent() {
					e.ConsecutiveFailures++
					fmt.Fprintf(os.Stderr, "%s: permanent-looking failure %d/%d: %v\n", e.Repo, e.ConsecutiveFailures, manifest.AutoPauseThreshold, err)

					if e.ConsecutiveFailures >= manifest.AutoPauseThreshold {
						e.State = manifest.StatePaused
						e.Note = fmt.Sprintf("auto-paused on %s: %v", time.Now().UTC().Format(chartdata.DateFormat), err)

						fmt.Fprintf(os.Stderr, "%s: auto-paused; unpause by re-running `gh star-charts add %s`\n", e.Repo, e.Repo)
					}
				} else {
					transientFailures = append(transientFailures, fmt.Sprintf("%s: %v", e.Repo, err))
					fmt.Fprintf(os.Stderr, "%s: transient failure: %v\n", e.Repo, err)
				}

				continue
			}

			e.ConsecutiveFailures = 0
			succeeded++
		}

		if succeeded == 0 && len(transientFailures) > 0 {
			// An all-failed run publishes nothing.
			return errors.New("every active chart failed; pushing nothing")
		}

		if err := m.Save(mPath); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		maybeNoteNewerRelease(c)

		_, err = r.CommitPush("chore: update star charts", ".")

		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)

		return 1
	}

	if len(transientFailures) > 0 {
		fmt.Fprintf(os.Stderr, "completed with %d transient failure(s):\n  %s\n", len(transientFailures), strings.Join(transientFailures, "\n  "))

		return 1
	}

	fmt.Println("all charts updated")

	return 0
}

// refreshOne fetches current metadata for one entry, verifies identity, and
// re-renders. lastChecked advances only on an ID-verified success.
func refreshOne(c *ghapi.Client, r *instance.Repo, e *manifest.Entry) error {
	meta, err := c.GetRepo(e.Repo)
	if err != nil {
		return err
	}

	if meta.ID != e.RepoID {
		// The tracked name now belongs to a different repository: the rename
		// redirect is gone and the old name was reused. Recording its counts
		// would put a stranger's data on an established chart.
		return &ghapi.StatusError{StatusCode: 404, Message: fmt.Sprintf("name %s now resolves to repo id %d, expected %d (rename-and-reuse)", e.Repo, meta.ID, e.RepoID)}
	}

	if meta.Private {
		return &ghapi.StatusError{StatusCode: 404, Message: "repo became private"}
	}

	// Follow a plain rename: same ID, new canonical name. The chart path
	// stays frozen, URLs are forever.
	e.Repo = meta.FullName

	dataPath := filepath.Join(r.Dir, e.Path, "data.json")

	d, err := chartdata.Load(dataPath)
	if err != nil {
		return err
	}

	d.Repo = meta.FullName
	d.Observe(time.Now().UTC(), meta.StargazersCount)

	return writeChart(r.Dir, e.Path, d, e.Style)
}

// maybeNoteNewerRelease surfaces available upgrades: notification, never
// authority. Failures of this check are ignored.
func maybeNoteNewerRelease(c *ghapi.Client) {
	latest := c.LatestReleaseTag(instance.ProductRepo)
	if latest == "" || Version == "dev" || latest == Version {
		return
	}

	note := fmt.Sprintf("gh-star-charts %s is available (this instance runs %s): upgrade with `gh extension upgrade star-charts && gh star-charts init`", latest, Version)

	fmt.Fprintln(os.Stderr, "note: "+note)

	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		f, err := os.OpenFile(summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, note)
			f.Close()
		}
	}
}
