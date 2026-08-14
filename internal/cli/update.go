package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

// cmdUpdate is the CI entry point. It owns the complete transaction: clone,
// refresh every active chart, commit, push, and report the aggregate status.
// Successes are published even when other entries fail, and any failure makes
// the run exit non-zero so it stays visible. Permanent-shape failures
// additionally count toward auto-pause, so a deleted repo goes quiet after a
// few loud runs instead of staying red forever.
func cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	inst := fs.String("instance", "", "instance repo (owner/name)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

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

	failed := 0

	err = instance.Transact(target, c.Token(), func(r *instance.Repo) error {
		failed = 0

		m, mPath, err := requireInstance(r)
		if err != nil {
			return err
		}

		for i := range m.Charts {
			e := &m.Charts[i]
			if e.State != manifest.StateActive {
				continue
			}

			if err := refreshOne(c, r, e); err != nil {
				failed++

				if isPermanentFailure(err) {
					e.ConsecutiveFailures++
					fmt.Fprintf(os.Stderr, "%s: permanent-looking failure %d/%d: %v\n", e.Repo, e.ConsecutiveFailures, manifest.AutoPauseThreshold, err)

					if e.ConsecutiveFailures >= manifest.AutoPauseThreshold {
						e.State = manifest.StatePaused
						e.Note = fmt.Sprintf("auto-paused on %s: %v", time.Now().UTC().Format(chartdata.DateFormat), err)

						fmt.Fprintf(os.Stderr, "%s: auto-paused. Recover by re-running `gh star-charts add %s`\n", e.Repo, e.Repo)
					}
				} else {
					fmt.Fprintf(os.Stderr, "%s: transient failure: %v\n", e.Repo, err)
				}
			} else {
				e.ConsecutiveFailures = 0
			}
		}

		// Failure bookkeeping (counters, auto-pause) must land even on a bad
		// day, or a permanently broken entry could stay loud forever.
		if err := m.Save(mPath); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		_, err = r.CommitPush("chore: update star charts", ".")

		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)

		return 1
	}

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "completed with %d failed chart(s), see above\n", failed)

		return 1
	}

	fmt.Println("all charts updated")

	return 0
}

// isPermanentFailure classifies errors that retrying cannot fix: the repo is
// gone or out of reach, its identity changed, its data file is missing, or it
// was written by a newer binary than the one running.
func isPermanentFailure(err error) bool {
	var se *ghapi.StatusError
	if errors.As(err, &se) {
		return se.IsPermanent()
	}

	return errors.Is(err, os.ErrNotExist) || errors.Is(err, chartdata.ErrNewerSchema) || errors.Is(err, manifest.ErrNewerSchema)
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

	chartDir, err := r.ChartDir(e.Path)
	if err != nil {
		return err
	}

	d, err := chartdata.Load(filepath.Join(chartDir, "data.json"))
	if err != nil {
		return err
	}

	if d.RepoID != 0 && d.RepoID != e.RepoID {
		return fmt.Errorf("data file at %s belongs to repo id %d, expected %d, refusing to mix histories", e.Path, d.RepoID, e.RepoID)
	}

	d.RepoID = e.RepoID
	d.Repo = meta.FullName

	if err := d.Observe(time.Now().UTC(), meta.StargazersCount); err != nil {
		return err
	}

	return writeChart(chartDir, d, e.Style)
}
