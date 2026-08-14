package cli

import (
	"flag"
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
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	chartsRepo := fs.String("charts-repo", "", "instance repo, default <login>/star-charts")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
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
		fmt.Println("no charts tracked yet. Track one with `gh star-charts add owner/repo`")

		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tSTATE\tSTARS\tLAST CHECKED\tDETAIL")

	for _, e := range m.Charts {
		stars, lastChecked := "-", "-"

		if chartDir, err := r.ChartDir(e.Path); err == nil {
			if d, err := chartdata.Load(filepath.Join(chartDir, "data.json")); err == nil {
				if d.LastChecked != "" {
					lastChecked = d.LastChecked
				}

				if len(d.Points) > 0 {
					stars = strconv.Itoa(d.Points[len(d.Points)-1].Stars)
				}
			} else {
				lastChecked = "data unreadable"
			}
		}

		state, detail := e.State, e.Note

		if e.State == manifest.StateActive && e.ConsecutiveFailures > 0 {
			state = "failing"
			detail = fmt.Sprintf("%d/%d failures before auto-pause", e.ConsecutiveFailures, manifest.AutoPauseThreshold)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Repo, state, stars, lastChecked, detail)
	}

	return w.Flush()
}
