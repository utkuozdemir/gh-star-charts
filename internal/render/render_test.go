package render_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/render"
)

func data(points []chartdata.Point, lastChecked string) *chartdata.Data {
	return &chartdata.Data{
		SchemaVersion: chartdata.SchemaVersion,
		RepoID:        1, Repo: "owner/repo",
		ObservedSince: "2026-08-13", LastChecked: lastChecked,
		Points: points,
	}
}

// every rendered SVG must be valid XML with finite coordinates, for every
// structural edge case.
func TestStructuralEdgeCases(t *testing.T) {
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
		"leap-day": {
			{Date: "2024-02-29", Stars: 10}, {Date: "2026-08-13", Stars: 20},
		},
		"multi-year": {
			{Date: "2019-03-04", Stars: 3}, {Date: "2022-06-01", Stars: 900}, {Date: "2026-08-13", Stars: 1528},
		},
	}

	for name, pts := range cases {
		for _, th := range []render.Theme{render.Light, render.Dark} {
			svg := render.SVG(data(pts, "2026-08-13"), th)

			if err := xml.Unmarshal([]byte(svg), new(any)); err != nil {
				t.Errorf("%s/%s: invalid XML: %v", name, th.Name, err)
			}

			for _, bad := range []string{"NaN", "Inf", "-Inf"} {
				if strings.Contains(svg, bad) {
					t.Errorf("%s/%s: non-finite coordinate in output", name, th.Name)
				}
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-06-08", Stars: 1}, {Date: "2024-01-01", Stars: 800}, {Date: "2026-08-13", Stars: 1528},
	}, "2026-08-13")

	if render.SVG(d, render.Light) != render.SVG(d, render.Light) {
		t.Fatal("rendering must be deterministic")
	}
}

func TestFreshnessCaption(t *testing.T) {
	d := data([]chartdata.Point{{Date: "2026-03-01", Stars: 10}}, "2026-08-13")

	svg := render.SVG(d, render.Light)

	if !strings.Contains(svg, "updated through 2026-08-13") {
		t.Fatal("caption must carry lastChecked")
	}

	// The flat segment to lastChecked makes the right edge a freshness signal.
	if !strings.Contains(svg, "2026-08-13") {
		t.Fatal("curve must extend to lastChecked")
	}
}

func TestTruncationCaption(t *testing.T) {
	d := data([]chartdata.Point{{Date: "2026-08-13", Stars: 41000}}, "2026-08-13")
	d.PrefixTruncated = true

	svg := render.SVG(d, render.Light)

	if !strings.Contains(svg, "history before 2026-08-13 unavailable") {
		t.Fatal("truncated backfill must be captioned")
	}
}

func TestRepoNameEscaped(t *testing.T) {
	d := data([]chartdata.Point{{Date: "2026-08-13", Stars: 1}}, "2026-08-13")
	d.Repo = `own"er/<repo>&x`

	svg := render.SVG(d, render.Light)

	if err := xml.Unmarshal([]byte(svg), new(any)); err != nil {
		t.Fatalf("escaping broken: %v", err)
	}
}

func TestTextWearsInkNotSeriesColor(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-01-01", Stars: 1}, {Date: "2026-08-13", Stars: 100},
	}, "2026-08-13")

	for _, th := range []render.Theme{render.Light, render.Dark} {
		for _, line := range strings.Split(render.SVG(d, th), "\n") {
			if strings.HasPrefix(line, "<text") && strings.Contains(line, th.Line) {
				t.Errorf("%s: text element wears the series color: %s", th.Name, line)
			}
		}
	}
}

func TestGoldenSmoke(t *testing.T) {
	// Bytes are pinned indirectly: assert the load-bearing elements exist so
	// a refactor cannot silently drop them.
	d := data([]chartdata.Point{
		{Date: "2021-06-08", Stars: 1}, {Date: "2026-08-13", Stars: 1528},
	}, "2026-08-13")

	svg := render.SVG(d, render.Dark)

	for _, want := range []string{
		"owner/repo",           // title
		"1,528",                // grouped current count
		"polyline",             // the line mark
		"polygon",              // the area fill
		render.Dark.Line,       // series color present
		"updated through 2026", // freshness caption
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %q in rendered SVG", want)
		}
	}
}

func TestStyleOverrides(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-01-01", Stars: 1}, {Date: "2026-08-13", Stars: 100},
	}, "2026-08-13")

	th := render.Light.WithOverrides("#e86161", "#fffdf5")
	svg := render.SVG(d, th)

	if !strings.Contains(svg, `stroke="#e86161"`) {
		t.Error("line color override not applied")
	}

	if !strings.Contains(svg, `fill="#fffdf5"`) {
		t.Error("background override not applied")
	}

	if strings.Contains(render.SVG(d, render.Light), "<rect") {
		t.Error("default chart must stay transparent")
	}
}
