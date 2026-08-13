// Package cli wires the subcommands. Interactive verbs (init, add, remove,
// reset) run under the user's ambient gh auth; update is the CI entry point.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
	"github.com/utkuozdemir/gh-star-charts/internal/render"
)

// Version is stamped at build time.
var Version = "dev"

// Run dispatches the subcommand and returns the process exit code.
func Run(args []string) int {
	if len(args) < 1 {
		usage()

		return 2
	}

	cmd, rest := args[0], args[1:]

	var err error

	switch cmd {
	case "init":
		err = cmdInit(rest)
	case "add":
		err = cmdAdd(rest)
	case "remove":
		err = cmdRemove(rest)
	case "reset":
		err = cmdReset(rest)
	case "update":
		return cmdUpdate(rest)
	case "version", "--version", "-v":
		fmt.Println(Version)

		return 0
	default:
		usage()

		return 2
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)

		return 1
	}

	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: gh star-charts <init|add|remove|reset|update|version> [args]

  init [owner/repo ...]      create or repair the instance repo, then add
  add owner/repo [...]       track repos: backfill, render, publish
  remove [--purge] owner/repo [...]
                             pause a chart (default keeps its files and URLs)
  reset owner/repo           destructive re-backfill, replaces observed history
  update --instance owner/repo
                             CI entry point: refresh every active chart`)
}

func newClient() (*ghapi.Client, error) {
	if h := ghapi.AuthenticatedHost(); h != "github.com" {
		return nil, fmt.Errorf("active gh host is %q: only github.com is supported", h)
	}

	return ghapi.New()
}

// defaultInstance resolves the instance repo name for interactive commands.
func defaultInstance(c *ghapi.Client, override string) (string, error) {
	if override != "" {
		if strings.Contains(override, "/") {
			return override, nil
		}

		login, err := c.AuthenticatedLogin()
		if err != nil {
			return "", err
		}

		return login + "/" + override, nil
	}

	login, err := c.AuthenticatedLogin()
	if err != nil {
		return "", err
	}

	return login + "/star-charts", nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo (name or owner/name), default <login>/star-charts")
	pinVersion := fs.String("pin-version", "", "release version to pin in the workflow (default: this binary's version)")
	cron := fs.String("cron", "", "update schedule as a cron expression (default daily; stored, so later repairs keep it)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	inst, err := defaultInstance(c, *chartsRepo)
	if err != nil {
		return err
	}

	// Create the instance repo when missing.
	if _, err := c.GetRepo(inst); err != nil {
		var se *ghapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
			return err
		}

		name := inst[strings.Index(inst, "/")+1:]

		fmt.Printf("creating public repo %s\n", inst)

		if _, err := c.CreateUserRepo(name, "Self-hosted GitHub star history charts, updated daily by gh-star-charts"); err != nil {
			return err
		}

		// Repo creation is eventually consistent; give the clone a moment.
		time.Sleep(2 * time.Second)
	}

	version, sum, err := resolvePin(*pinVersion)
	if err != nil {
		return err
	}

	err = instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		if !instance.LooksLikeInstance(r.Dir) {
			return fmt.Errorf("%s does not look like a star-charts instance repo; refusing to adopt it", inst)
		}

		m, err := manifest.Load(filepath.Join(r.Dir, manifest.FileName))
		if err != nil {
			return err
		}

		if *cron != "" {
			if !cronRe.MatchString(*cron) {
				return fmt.Errorf("invalid cron expression %q", *cron)
			}

			m.Cron = *cron
		}

		if err := m.Save(filepath.Join(r.Dir, manifest.FileName)); err != nil {
			return err
		}

		wfDir := filepath.Join(r.Dir, ".github", "workflows")
		if err := os.MkdirAll(wfDir, 0o755); err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(wfDir, instance.WorkflowFile), []byte(instance.WorkflowYAML(version, sum, m.Cron)), 0o644); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, version); err != nil {
			return err
		}

		_, err = r.CommitPush("chore: install gh-star-charts "+version+" workflow", ".")

		return err
	})
	if err != nil {
		return err
	}

	// Clear a possible 60-day auto-disable under the user's own auth.
	if err := c.EnableWorkflow(inst, instance.WorkflowFile); err != nil {
		fmt.Printf("note: could not (re-)enable the workflow yet: %v\n", err)
	}

	fmt.Printf("instance %s ready: extension %s, workflow pinned to %s\n", inst, Version, version)
	fmt.Println("upgrades: gh extension upgrade star-charts && gh star-charts init")

	if fs.NArg() > 0 {
		return addRepos(c, inst, fs.Args(), "", manifest.Style{}, "")
	}

	return nil
}

// resolvePin determines the release version and asset checksum to inline into
// the workflow. It downloads the checksums file for that release once, at
// write time, so the workflow itself never trusts a run-time fetch.
func resolvePin(override string) (version, sha string, err error) {
	version = override
	if version == "" {
		version = Version
	}

	if version == "dev" {
		return "", "", errors.New("a dev build cannot pin a workflow; pass --pin-version <released version>")
	}

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", instance.ProductRepo, version)

	resp, err := http.Get(url) //nolint:gosec // fixed product-repo URL
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release %s has no checksums.txt (HTTP %d)", version, resp.StatusCode)
	}

	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)

	for _, line := range strings.Split(string(buf[:n]), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasSuffix(fields[1], "linux-amd64") {
			return version, fields[0], nil
		}
	}

	return "", "", fmt.Errorf("no linux-amd64 checksum in release %s", version)
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")
	chartPath := fs.String("chart-path", "", "override the chart directory (only for path collisions)")

	var style manifest.Style

	fs.StringVar(&style.LineColor, "line-color", "", "per-chart line color (both modes unless -line-color-dark is set); \"none\" clears")
	fs.StringVar(&style.LineColorDark, "line-color-dark", "", "line color for the dark chart")
	fs.StringVar(&style.Background, "background", "", "explicit chart background (default transparent); \"none\" clears")
	fs.StringVar(&style.BackgroundDark, "background-dark", "", "background for the dark chart")

	look := fs.String("look", "", "chart style: sketchy (the classic hand-drawn default) or clean")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return errors.New("add: give at least one owner/repo")
	}

	if *chartPath != "" && fs.NArg() > 1 {
		return errors.New("--chart-path only makes sense with a single repo")
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	inst, err := defaultInstance(c, *chartsRepo)
	if err != nil {
		return err
	}

	if _, err := c.GetRepo(inst); err != nil {
		return fmt.Errorf("instance repo %s not reachable (run `gh star-charts init` first): %w", inst, err)
	}

	return addRepos(c, inst, fs.Args(), *chartPath, style, *look)
}

func addRepos(c *ghapi.Client, inst string, repos []string, pathOverride string, style manifest.Style, look string) error {
	for _, arg := range repos {
		if err := addOne(c, inst, arg, pathOverride, style, look); err != nil {
			return fmt.Errorf("add %s: %w", arg, err)
		}
	}

	return nil
}

// applyStyle merges add-time style flags onto an entry: empty flags keep the
// stored style, the sentinel "none" clears a field.
func applyStyle(entry *manifest.Entry, flags manifest.Style) {
	apply := func(dst *string, v string) {
		switch v {
		case "":
		case "none":
			*dst = ""
		default:
			*dst = v
		}
	}

	apply(&entry.Style.LineColor, flags.LineColor)
	apply(&entry.Style.LineColorDark, flags.LineColorDark)
	apply(&entry.Style.Background, flags.Background)
	apply(&entry.Style.BackgroundDark, flags.BackgroundDark)
}

func addOne(c *ghapi.Client, inst, repoArg, pathOverride string, style manifest.Style, look string) error {
	meta, err := c.GetRepo(repoArg)
	if err != nil {
		return describeAccessError(err)
	}

	if meta.Private {
		return fmt.Errorf("%s is private: the instance repo's workflow could never read its star count, and its chart would publish private data; only public repos are supported", meta.FullName)
	}

	var snippet string

	err = instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		mPath := filepath.Join(r.Dir, manifest.FileName)

		m, err := manifest.Load(mPath)
		if err != nil {
			return err
		}

		entry := m.FindByID(meta.ID)

		if entry == nil {
			chartPath := pathOverride
			if chartPath == "" {
				chartPath = manifest.ChartPath(meta.FullName)
			}

			if taken := m.FindByPath(chartPath); taken != nil {
				return fmt.Errorf("chart path %s is already owned by %s (repo id %d); pass --chart-path to place this chart elsewhere", chartPath, taken.Repo, taken.RepoID)
			}

			m.Charts = append(m.Charts, manifest.Entry{
				RepoID: meta.ID, Repo: meta.FullName, Path: chartPath, State: manifest.StateActive,
			})
			entry = m.FindByID(meta.ID)
		} else {
			// Repair semantics: refresh canonical name after a rename, keep
			// the frozen path, reactivate.
			entry.Repo = meta.FullName
			entry.State = manifest.StateActive
			entry.ConsecutiveFailures = 0
			entry.Note = ""
		}

		applyStyle(entry, style)

		switch look {
		case "":
		case "sketchy", "clean":
			entry.Style.Look = look
		default:
			return fmt.Errorf("-look takes sketchy or clean, got %q", look)
		}

		if err := validStyle(entry.Style); err != nil {
			return err
		}

		dataPath := filepath.Join(r.Dir, entry.Path, "data.json")

		d, err := chartdata.Load(dataPath)
		if errors.Is(err, os.ErrNotExist) {
			d = &chartdata.Data{SchemaVersion: chartdata.SchemaVersion, RepoID: meta.ID, Repo: meta.FullName}
		} else if err != nil {
			return err
		}

		d.Repo = meta.FullName

		// Prefix backfill is once-only: a fresh survivor reconstruction on a
		// chart with observed history would silently rewrite the published
		// curve. That is reset's job.
		if len(d.Points) == 0 {
			fmt.Printf("backfilling %s (%d stars)\n", meta.FullName, meta.StargazersCount)

			bf, err := c.Backfill(meta.FullName, meta.StargazersCount)
			if err != nil {
				return describeAccessError(err)
			}

			if bf.Truncated {
				fmt.Printf("note: %s exceeds the API's 40k-star history cap; the chart will honestly start today\n", meta.FullName)

				d.PrefixTruncated = true
			} else if err := d.SetPrefix(chartdata.BuildPrefix(bf.StarredAt)); err != nil {
				return err
			}
		}

		// Always land an authoritative current-count observation.
		d.Observe(time.Now().UTC(), meta.StargazersCount)

		if err := writeChart(r.Dir, entry.Path, d, entry.Style); err != nil {
			return err
		}

		if err := m.Save(mPath); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		// Save sorts the entries slice, so re-resolve by ID rather than
		// trusting a pre-sort pointer.
		snippet = instance.EmbedSnippet(r, *m.FindByID(meta.ID))

		_, err = r.CommitPush(fmt.Sprintf("chore: add star chart for %s", meta.FullName), ".")

		return err
	})
	if err != nil {
		return err
	}

	fmt.Printf("\n%s is now tracked. Embed its chart with:\n\n%s\n\n", meta.FullName, snippet)

	return nil
}

func describeAccessError(err error) error {
	var se *ghapi.StatusError
	if !errors.As(err, &se) {
		return err
	}

	switch {
	case se.SSO:
		return fmt.Errorf("%w\nthe organization requires single-sign-on authorization for your token: authorize it and retry", err)
	case se.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(se.Message), "rate limit"):
		return fmt.Errorf("%w\nrate limited: wait a bit and retry", err)
	case se.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w\nreading star history requires write access on the repo (GitHub restricted this data in June 2026); accepted permissions: %s", err, se.AcceptedPermissions)
	case se.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w\nthe repo does not exist, or your credentials cannot see it", err)
	default:
		return err
	}
}

// colorRe loosely accepts CSS color values while keeping SVG attribute
// injection impossible.
var colorRe = regexp.MustCompile(`^[-#a-zA-Z0-9(),.% ]+$`)

// cronRe loosely validates a five-field cron expression and keeps YAML
// injection out of the generated workflow.
var cronRe = regexp.MustCompile(`^[0-9*,/ -]+$`)

func validStyle(s manifest.Style) error {
	for _, v := range []string{s.LineColor, s.LineColorDark, s.Background, s.BackgroundDark} {
		if v != "" && !colorRe.MatchString(v) {
			return fmt.Errorf("invalid color value %q", v)
		}
	}

	return nil
}

// themesFor applies per-chart style on top of the validated defaults. A
// single value covers both modes unless its dark variant is set.
func themesFor(s manifest.Style) []render.Theme {
	darkLine := s.LineColorDark
	if darkLine == "" {
		darkLine = s.LineColor
	}

	darkBg := s.BackgroundDark
	if darkBg == "" {
		darkBg = s.Background
	}

	light := render.Light.WithOverrides(s.LineColor, s.Background)
	dark := render.Dark.WithOverrides(darkLine, darkBg)

	// The classic hand-drawn look is the default; "clean" opts out.
	if s.Look != "clean" {
		light = light.WithSketchy(render.SketchyLineLight)
		dark = dark.WithSketchy(render.SketchyLineDark)
	}

	return []render.Theme{light, dark}
}

func writeChart(root, chartPath string, d *chartdata.Data, style manifest.Style) error {
	dir := filepath.Join(root, chartPath)

	if err := d.Save(filepath.Join(dir, "data.json")); err != nil {
		return err
	}

	for _, th := range themesFor(style) {
		tmp, err := os.CreateTemp(dir, ".svg-*")
		if err != nil {
			return err
		}

		if _, err := tmp.WriteString(render.SVG(d, th)); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())

			return err
		}

		if err := tmp.Close(); err != nil {
			return err
		}

		if err := os.Rename(tmp.Name(), filepath.Join(dir, th.Name+".svg")); err != nil {
			return err
		}
	}

	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")
	purge := fs.Bool("purge", false, "also delete the chart files; their URLs will 404")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return errors.New("remove: give at least one owner/repo")
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	inst, err := defaultInstance(c, *chartsRepo)
	if err != nil {
		return err
	}

	return instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		mPath := filepath.Join(r.Dir, manifest.FileName)

		m, err := manifest.Load(mPath)
		if err != nil {
			return err
		}

		for _, arg := range fs.Args() {
			var entry *manifest.Entry

			for i := range m.Charts {
				if strings.EqualFold(m.Charts[i].Repo, arg) {
					entry = &m.Charts[i]
				}
			}

			if entry == nil {
				return fmt.Errorf("%s is not tracked", arg)
			}

			if *purge {
				if err := os.RemoveAll(filepath.Join(r.Dir, entry.Path)); err != nil {
					return err
				}

				kept := m.Charts[:0]

				for _, e := range m.Charts {
					if e.RepoID != entry.RepoID {
						kept = append(kept, e)
					}
				}

				m.Charts = kept

				fmt.Printf("purged %s: its chart URLs now 404\n", arg)
			} else {
				entry.State = manifest.StatePaused
				entry.Note = "removed by owner"

				fmt.Printf("paused %s: chart files and URLs keep serving their last state\n", arg)
			}
		}

		if err := m.Save(mPath); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		_, err = r.CommitPush("chore: remove star charts: "+strings.Join(fs.Args(), ", "), ".")

		return err
	})
}

func cmdReset(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("reset: give exactly one owner/repo")
	}

	if !*yes {
		fmt.Printf("reset replaces %s's observed history with a fresh reconstruction; recorded unstar dips are lost (recoverable only via git history). Type the repo name to confirm: ", fs.Arg(0))

		var confirm string

		fmt.Scanln(&confirm)

		if !strings.EqualFold(confirm, fs.Arg(0)) {
			return errors.New("aborted")
		}
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	inst, err := defaultInstance(c, *chartsRepo)
	if err != nil {
		return err
	}

	return instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		mPath := filepath.Join(r.Dir, manifest.FileName)

		m, err := manifest.Load(mPath)
		if err != nil {
			return err
		}

		var entry *manifest.Entry

		for i := range m.Charts {
			if strings.EqualFold(m.Charts[i].Repo, fs.Arg(0)) {
				entry = &m.Charts[i]
			}
		}

		if entry == nil {
			return fmt.Errorf("%s is not tracked", fs.Arg(0))
		}

		meta, err := c.GetRepo(entry.Repo)
		if err != nil {
			return describeAccessError(err)
		}

		if meta.ID != entry.RepoID {
			return fmt.Errorf("%s now resolves to a different repository (id %d, expected %d); refusing", entry.Repo, meta.ID, entry.RepoID)
		}

		bf, err := c.Backfill(meta.FullName, meta.StargazersCount)
		if err != nil {
			return describeAccessError(err)
		}

		d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion, RepoID: meta.ID, Repo: meta.FullName, PrefixTruncated: bf.Truncated}

		if !bf.Truncated {
			if err := d.SetPrefix(chartdata.BuildPrefix(bf.StarredAt)); err != nil {
				return err
			}
		}

		d.Observe(time.Now().UTC(), meta.StargazersCount)

		if err := writeChart(r.Dir, entry.Path, d, entry.Style); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		_, err = r.CommitPush("chore: reset star chart for "+meta.FullName, ".")

		return err
	})
}
