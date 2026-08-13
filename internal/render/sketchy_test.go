package render_test

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/render"
)

func sketchyThemes() []render.Theme {
	return []render.Theme{
		render.Light.WithSketchy(render.SketchyLineLight),
		render.Dark.WithSketchy(render.SketchyLineDark),
	}
}

// The default look gets the full structural edge-case matrix, same as the
// clean one: valid XML and finite coordinates for every shape of data.
func TestSketchyStructuralEdgeCases(t *testing.T) {
	cases := map[string][]chartdata.Point{
		"empty":       {},
		"single":      {{Date: "2026-08-13", Stars: 5}},
		"single-zero": {{Date: "2026-08-13", Stars: 0}},
		"constant-zero": {
			{Date: "2026-08-01", Stars: 0}, {Date: "2026-08-13", Stars: 0},
		},
		"decrease": {
			{Date: "2026-08-01", Stars: 100}, {Date: "2026-08-02", Stars: 40}, {Date: "2026-08-13", Stars: 60},
		},
		"huge": {
			{Date: "2020-01-01", Stars: 1}, {Date: "2026-08-13", Stars: 393000},
		},
		"millions": {
			{Date: "2020-01-01", Stars: 1}, {Date: "2026-08-13", Stars: 2400000},
		},
		"leap-day": {
			{Date: "2024-02-29", Stars: 10}, {Date: "2026-08-13", Stars: 20},
		},
		"multi-year": {
			{Date: "2019-03-04", Stars: 3}, {Date: "2022-06-01", Stars: 900}, {Date: "2026-08-13", Stars: 1528},
		},
		"long-name": {
			{Date: "2026-08-13", Stars: 1},
		},
	}

	for name, pts := range cases {
		d := data(pts, "2026-08-13")
		if name == "long-name" {
			d.Repo = strings.Repeat("verylongowner/", 8) + "repo"
		}

		for _, th := range sketchyThemes() {
			svg := render.SVG(d, th)

			if err := xml.Unmarshal([]byte(svg), new(any)); err != nil {
				t.Errorf("%s/%s: invalid XML: %v", name, th.Name, err)
			}

			for _, bad := range []string{"NaN", "Inf"} {
				if strings.Contains(svg, bad) {
					t.Errorf("%s/%s: non-finite coordinate in output", name, th.Name)
				}
			}
		}
	}
}

func TestSketchyIsTheDefaultLookElements(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-06-08", Stars: 1}, {Date: "2024-01-01", Stars: 800}, {Date: "2026-08-13", Stars: 1528},
	}, "2026-08-13")

	svg := render.SVG(d, render.Light.WithSketchy(render.SketchyLineLight))

	for _, want := range []string{
		"Star History",          // the classic centered title
		"GitHub Stars",          // y axis title
		">Date<",                // x axis title
		"1.5k",                  // k-formatted tick
		"owner/repo",            // legend chip
		"Comic",                 // comic lettering stack
		render.SketchyLineLight, // coral line
		"updated through",       // freshness caption
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %q in default-look SVG", want)
		}
	}
}

func TestSketchyDeterministicAndSeedStable(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-06-08", Stars: 1}, {Date: "2026-08-13", Stars: 1528},
	}, "2026-08-13")

	th := render.Light.WithSketchy(render.SketchyLineLight)

	if render.SVG(d, th) != render.SVG(d, th) {
		t.Fatal("sketchy rendering must be deterministic")
	}

	// Different repos wobble differently (seeded by name), so identical data
	// for different repos still looks hand-drawn rather than stamped.
	d2 := data(d.Points, "2026-08-13")
	d2.Repo = "other/repo"

	if render.SVG(d, th) == render.SVG(d2, th) {
		t.Fatal("wobble seed must vary by repo")
	}
}

func TestDownsampleKeepsDips(t *testing.T) {
	// A year of flat 1000 with a single one-day crash to 100 must keep the
	// crash visible after downsampling to a small budget.
	var pts []chartdata.Point

	for i := 0; i < 400; i++ {
		stars := 1000
		if i == 200 {
			stars = 100
		}

		pts = append(pts, chartdata.Point{Date: dayOffset(i), Stars: stars})
	}

	d := data(pts, dayOffset(399))

	svg := render.SVG(d, render.Light.WithSketchy(render.SketchyLineLight))

	// The dip reaches far below every other point; its y coordinate region
	// (higher y value in SVG space) must appear in the path.
	if !strings.Contains(svg, "Star History") {
		t.Fatal("sanity")
	}

	// Rough but effective: a flat-only line has a tiny y-range in the path
	// data, the dip widens it. Assert the SVG differs from the no-dip render.
	for i := range pts {
		pts[i].Stars = 1000
	}

	flat := data(pts, dayOffset(399))
	if render.SVG(flat, render.Light.WithSketchy(render.SketchyLineLight)) == svg {
		t.Fatal("a one-day dip must survive polyline downsampling")
	}
}

func dayOffset(i int) string {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(chartdata.DateFormat)
}
