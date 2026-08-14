package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

// cmdList shows every tracked chart and its state. It is strictly read-only:
// a fresh shallow clone gives one coherent snapshot of the manifest and the
// per-chart data files together, and nothing is committed or pushed. A broken
// or missing data file degrades that entry's row instead of failing the whole
// listing, because the command is most useful when something is half-broken.
func cmdList(args []string) error {
	fs := newFlagSet("list", "list [flags]")
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")

	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if fs.NArg() > 0 {
		return fmt.Errorf("list takes no arguments, got %q", fs.Args())
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	inst, err := defaultInstance(c, *chartsRepo)
	if err != nil {
		return err
	}

	r, err := instance.Clone(inst, c.Token())
	if err != nil {
		return err
	}
	defer r.Close()

	m, _, err := requireInstance(r)
	if err != nil {
		return err
	}

	fmt.Printf("instance: https://github.com/%s\n\n", inst)

	if len(m.Charts) == 0 {
		fmt.Printf("no charts tracked yet. Track one with `gh star-charts add owner/repo --charts-repo %s`\n", inst)

		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tSTATE\tSTARS\tLAST CHECKED\tDETAIL")

	for _, e := range m.Charts {
		state, stars, lastChecked, detail := listRow(r, e)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Repo, state, stars, lastChecked, detail)
	}

	return w.Flush()
}

// listRow derives one table row from a manifest entry and its chart files. A
// broken data file degrades the whole row and says why in the detail column,
// never showing values it cannot vouch for. In particular, a data file whose
// pinned repo ID differs from the entry's is another repository's history, so
// its numbers must not appear under this entry's name (the updater refuses
// the same mismatch).
func listRow(r *instance.Repo, e manifest.Entry) (state, stars, lastChecked, detail string) {
	state, stars, lastChecked, detail = e.State, "-", "-", e.Note

	var dataNote string

	if chartDir, err := r.ChartDir(e.Path); err != nil {
		dataNote = "invalid chart path"
	} else if d, err := chartdata.Load(filepath.Join(chartDir, "data.json")); err != nil {
		dataNote = "data file missing or unreadable"
	} else if d.RepoID != 0 && d.RepoID != e.RepoID {
		dataNote = fmt.Sprintf("data file belongs to repo id %d, expected %d", d.RepoID, e.RepoID)
	} else {
		if d.LastChecked != "" {
			lastChecked = d.LastChecked
		}

		if len(d.Points) > 0 {
			stars = strconv.Itoa(d.Points[len(d.Points)-1].Stars)
		}
	}

	if e.State == manifest.StateActive && e.ConsecutiveFailures > 0 {
		state = "failing"
		detail = fmt.Sprintf("%d/%d failures before auto-pause", e.ConsecutiveFailures, manifest.AutoPauseThreshold)
	}

	if dataNote != "" {
		if detail != "" {
			detail += ", " + dataNote
		} else {
			detail = dataNote
		}
	}

	return state, stars, lastChecked, detail
}
