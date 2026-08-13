// Package cli wires the subcommands. Interactive verbs (init, add, remove,
// reset) run under the user's ambient gh auth; update is the CI entry point.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
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

// boolFlags names the flags that take no value, which argument normalization
// needs to know.
var boolFlags = map[string]bool{"purge": true, "yes": true}

// normalizeArgs lets flags appear after positional arguments, the way the
// docs show them, by partitioning the argument list before flag parsing.
func normalizeArgs(args []string) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			positional = append(positional, a)

			continue
		}

		flags = append(flags, a)

		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}

		if !boolFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}

func newClient() (*ghapi.Client, error) {
	if h := ghapi.AuthenticatedHost(); h != "github.com" {
		return nil, fmt.Errorf("active gh host is %q: only github.com is supported", h)
	}

	return ghapi.New()
}

// defaultInstance resolves the instance repo name for interactive commands.
func defaultInstance(c *ghapi.Client, override string) (string, error) {
	if override != "" && strings.Contains(override, "/") {
		return override, nil
	}

	login, err := c.AuthenticatedLogin()
	if err != nil {
		return "", err
	}

	if override != "" {
		return login + "/" + override, nil
	}

	return login + "/star-charts", nil
}

// requireInstance loads the manifest of an already-initialized instance repo.
// Every mutating command except init goes through it, so a mistyped repo name
// can never be written into.
func requireInstance(r *instance.Repo) (*manifest.Manifest, string, error) {
	mPath := filepath.Join(r.Dir, manifest.FileName)

	if _, err := os.Stat(mPath); errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("%s has no %s, so it is not a star-charts instance repo. Run `gh star-charts init` first", r.FullName, manifest.FileName)
	}

	m, err := manifest.Load(mPath)
	if err != nil {
		return nil, "", err
	}

	return m, mPath, nil
}

// checkInstanceRepo verifies the instance repo is usable: reachable and
// public, since raw URLs from a private repo are not embeddable.
func checkInstanceRepo(c *ghapi.Client, inst string) error {
	meta, err := c.GetRepo(inst)
	if err != nil {
		return err
	}

	if meta.Private {
		return fmt.Errorf("instance repo %s is private, so its raw image URLs would return 404 to readers. Use a public repo", inst)
	}

	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo (name or owner/name), default <login>/star-charts")
	pinVersion := fs.String("pin-version", "", "release version to pin in the workflow (default: this binary's version)")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
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

	login, err := c.AuthenticatedLogin()
	if err != nil {
		return err
	}

	// Create the instance repo when missing, but only under the user's own
	// account: auto-creating anywhere else would either fail after leaving a
	// stray repo behind or surprise an organization.
	if _, err := c.GetRepo(inst); err != nil {
		var se *ghapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
			return err
		}

		owner := inst[:strings.Index(inst, "/")]
		if !strings.EqualFold(owner, login) {
			return fmt.Errorf("%s does not exist, and repos are only auto-created under your own account (%s). Create it first, then re-run", inst, login)
		}

		fmt.Printf("creating public repo %s\n", inst)

		if _, err := c.CreateUserRepo(inst[strings.Index(inst, "/")+1:], "Self-hosted GitHub star history charts, updated daily by gh-star-charts"); err != nil {
			return err
		}

		// Repo creation is eventually consistent; give the clone a moment.
		time.Sleep(2 * time.Second)
	}

	if err := checkInstanceRepo(c, inst); err != nil {
		return err
	}

	version, sum, err := resolvePin(*pinVersion)
	if err != nil {
		return err
	}

	err = instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		if !instance.LooksLikeInstance(r.Dir) {
			return fmt.Errorf("%s does not look like a star-charts instance repo, refusing to adopt it", inst)
		}

		m, err := manifest.Load(filepath.Join(r.Dir, manifest.FileName))
		if err != nil {
			return err
		}

		if err := m.Save(filepath.Join(r.Dir, manifest.FileName)); err != nil {
			return err
		}

		if err := writeWorkflow(r, version, sum); err != nil {
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

func writeWorkflow(r *instance.Repo, version, sum string) error {
	wfDir := filepath.Join(r.Dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(wfDir, instance.WorkflowFile), []byte(instance.WorkflowYAML(version, sum)), 0o644)
}

// ensureWorkflowCurrent repairs the workflow pin when the running binary is a
// released version that differs from the pinned one. This is what makes add
// an upgrade path, and it prevents version skew where a newer local binary
// writes files an older pinned updater would mangle or refuse.
func ensureWorkflowCurrent(r *instance.Repo) error {
	if Version == "dev" {
		return nil
	}

	wfPath := filepath.Join(r.Dir, ".github", "workflows", instance.WorkflowFile)

	existing, err := os.ReadFile(wfPath)
	if err == nil && strings.Contains(string(existing), "/"+Version+"/") {
		return nil
	}

	version, sum, err := resolvePin("")
	if err != nil {
		return fmt.Errorf("repair workflow pin: %w", err)
	}

	fmt.Printf("updating the instance workflow pin to %s\n", version)

	return writeWorkflow(r, version, sum)
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// resolvePin determines the release version and asset checksum to inline into
// the workflow. It downloads the checksums file for that release once, at
// write time, so the workflow itself never trusts a run-time fetch.
func resolvePin(override string) (version, sha string, err error) {
	version = override
	if version == "" {
		version = Version
	}

	if version == "dev" {
		return "", "", errors.New("a dev build cannot pin a workflow. Pass --pin-version <released version>")
	}

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", instance.ProductRepo, version)

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release %s has no checksums.txt (HTTP %d)", version, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}

	want := fmt.Sprintf("gh-star-charts_%s_linux-amd64", version)

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == want {
			if !hexRe.MatchString(fields[0]) {
				return "", "", fmt.Errorf("malformed checksum for %s in release %s", want, version)
			}

			return version, fields[0], nil
		}
	}

	return "", "", fmt.Errorf("no %s checksum in release %s", want, version)
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")
	chartPath := fs.String("chart-path", "", "override the chart directory (only for path collisions)")

	var style manifest.Style

	fs.StringVar(&style.LineColor, "line-color", "", "per-chart line color (both modes unless -line-color-dark is set); \"none\" clears")
	fs.StringVar(&style.LineColorDark, "line-color-dark", "", "line color for the dark chart")
	look := fs.String("look", "", "chart style: sketchy (the classic hand-drawn default), clean, or default to clear")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return errors.New("add: give at least one owner/repo")
	}

	if *chartPath != "" && fs.NArg() > 1 {
		return errors.New("--chart-path only makes sense with a single repo")
	}

	if *chartPath != "" {
		if err := manifest.ValidateChartPath(*chartPath); err != nil {
			return err
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

	if err := checkInstanceRepo(c, inst); err != nil {
		return fmt.Errorf("instance repo %s not usable (run `gh star-charts init` first): %w", inst, err)
	}

	return addRepos(c, inst, fs.Args(), *chartPath, style, *look)
}

func addRepos(c *ghapi.Client, inst string, repos []string, pathOverride string, style manifest.Style, look string) error {
	for _, arg := range repos {
		if err := addOne(c, inst, arg, pathOverride, style, look); err != nil {
			return fmt.Errorf("add %s: %w", arg, err)
		}
	}

	// Clear a possible 60-day auto-disable while we are here.
	if err := c.EnableWorkflow(inst, instance.WorkflowFile); err != nil {
		fmt.Printf("note: could not (re-)enable the workflow: %v\n", err)
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
}

func addOne(c *ghapi.Client, inst, repoArg, pathOverride string, style manifest.Style, look string) error {
	meta, err := c.GetRepo(repoArg)
	if err != nil {
		return describeAccessError(err)
	}

	if meta.Private {
		return fmt.Errorf("%s is private. The chart would publish its star counts, and the update workflow could not read them anyway, so only public repos are supported", meta.FullName)
	}

	var snippet string

	err = instance.Transact(inst, c.Token(), func(r *instance.Repo) error {
		m, mPath, err := requireInstance(r)
		if err != nil {
			return err
		}

		if err := ensureWorkflowCurrent(r); err != nil {
			return err
		}

		entry := m.FindByID(meta.ID)
		existing := entry != nil

		if entry == nil {
			chartPath := pathOverride
			if chartPath == "" {
				chartPath = manifest.ChartPath(meta.FullName)
			}

			if taken := m.FindByPath(chartPath); taken != nil {
				return fmt.Errorf("chart path %s is already owned by %s (repo id %d). Pass --chart-path to place this chart elsewhere", chartPath, taken.Repo, taken.RepoID)
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
		case "default":
			entry.Style.Look = ""
		default:
			return fmt.Errorf("-look takes sketchy, clean, or default, got %q", look)
		}

		if err := validStyle(entry.Style); err != nil {
			return err
		}

		chartDir, err := r.ChartDir(entry.Path)
		if err != nil {
			return err
		}

		dataPath := filepath.Join(chartDir, "data.json")

		d, err := chartdata.Load(dataPath)
		if errors.Is(err, os.ErrNotExist) {
			if existing {
				// A tracked entry whose data file is gone means it was
				// deleted by hand; the rebuild below is a survivor
				// reconstruction, worth saying out loud.
				fmt.Printf("note: %s has no data file, rebuilding its history from the surviving stars\n", meta.FullName)
			}

			d = &chartdata.Data{SchemaVersion: chartdata.SchemaVersion, RepoID: meta.ID, Repo: meta.FullName}
		} else if err != nil {
			return err
		}

		if d.RepoID != 0 && d.RepoID != meta.ID {
			return fmt.Errorf("data file at %s belongs to repo id %d, expected %d, refusing to mix histories", entry.Path, d.RepoID, meta.ID)
		}

		d.RepoID = meta.ID
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
				fmt.Printf("note: the API serves at most the first 40k stars of %s, so the chart will start today and say so\n", meta.FullName)

				d.PrefixTruncated = true
			} else if err := d.SetPrefix(chartdata.BuildPrefix(bf.StarredAt)); err != nil {
				return err
			}
		}

		// Always land an authoritative current-count observation.
		if err := d.Observe(time.Now().UTC(), meta.StargazersCount); err != nil {
			return err
		}

		if err := writeChart(chartDir, d, entry.Style); err != nil {
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

func validStyle(s manifest.Style) error {
	for _, v := range []string{s.LineColor, s.LineColorDark} {
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

	light := render.Light.WithOverrides(s.LineColor, "")
	dark := render.Dark.WithOverrides(darkLine, "")

	// The classic hand-drawn look is the default; "clean" opts out.
	if s.Look != "clean" {
		light = light.WithSketchy(render.SketchyLineLight)
		dark = dark.WithSketchy(render.SketchyLineDark)
	}

	return []render.Theme{light, dark}
}

func writeChart(chartDir string, d *chartdata.Data, style manifest.Style) error {
	if err := d.Save(filepath.Join(chartDir, "data.json")); err != nil {
		return err
	}

	for _, th := range themesFor(style) {
		tmp, err := os.CreateTemp(chartDir, ".svg-*")
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

		if err := os.Rename(tmp.Name(), filepath.Join(chartDir, th.Name+".svg")); err != nil {
			return err
		}
	}

	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")
	purge := fs.Bool("purge", false, "also delete the chart files; their URLs will 404")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
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
		m, mPath, err := requireInstance(r)
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
				chartDir, err := r.ChartDir(entry.Path)
				if err != nil {
					return err
				}

				if err := os.RemoveAll(chartDir); err != nil {
					return err
				}

				targetID := entry.RepoID
				kept := make([]manifest.Entry, 0, len(m.Charts))

				for _, e := range m.Charts {
					if e.RepoID != targetID {
						kept = append(kept, e)
					}
				}

				m.Charts = kept

				fmt.Printf("purged %s, its chart URLs now return 404\n", arg)
			} else {
				entry.State = manifest.StatePaused
				entry.Note = "removed by owner"

				fmt.Printf("paused %s, its chart files and URLs keep serving the last state\n", arg)
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

	if err := fs.Parse(normalizeArgs(args)); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("reset: give exactly one owner/repo")
	}

	if !*yes {
		fmt.Printf("reset replaces the observed history of %s with a fresh reconstruction. Recorded unstar dips are lost (recoverable only via the git history). Type the repo name to confirm: ", fs.Arg(0))

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
		m, mPath, err := requireInstance(r)
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
			return fmt.Errorf("%s now resolves to a different repository (id %d, expected %d), refusing", entry.Repo, meta.ID, entry.RepoID)
		}

		entry.Repo = meta.FullName

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

		if err := d.Observe(time.Now().UTC(), meta.StargazersCount); err != nil {
			return err
		}

		chartDir, err := r.ChartDir(entry.Path)
		if err != nil {
			return err
		}

		if err := writeChart(chartDir, d, entry.Style); err != nil {
			return err
		}

		if err := m.Save(mPath); err != nil {
			return err
		}

		if err := instance.WriteReadme(r, m, Version); err != nil {
			return err
		}

		_, err = r.CommitPush("chore: reset star chart for "+meta.FullName, ".")

		return err
	})
}
