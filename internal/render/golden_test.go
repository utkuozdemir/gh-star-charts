package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/render"
)

// Golden files pin the exact bytes of representative charts in both looks and
// both themes, so a renderer change cannot silently alter every published
// chart: the diff of these files is the review step. Regenerate with
// UPDATE_GOLDEN=1 go test ./internal/render/ and read the diff.
func TestGolden(t *testing.T) {
	d := data([]chartdata.Point{
		{Date: "2021-06-08", Stars: 2}, {Date: "2022-03-01", Stars: 160},
		{Date: "2023-06-01", Stars: 500}, {Date: "2024-12-01", Stars: 1020},
		{Date: "2026-08-13", Stars: 1528},
	}, "2026-08-13")

	cases := map[string]render.Theme{
		"sketchy-light.svg": render.Light.WithSketchy(render.SketchyLineLight),
		"sketchy-dark.svg":  render.Dark.WithSketchy(render.SketchyLineDark),
		"clean-light.svg":   render.Light,
		"clean-dark.svg":    render.Dark,
	}

	for name, th := range cases {
		got := render.SVG(d, th)
		path := filepath.Join("testdata", name)

		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}

			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing golden %s; run with UPDATE_GOLDEN=1 and review the diff", name)
		}

		if got != string(want) {
			t.Errorf("%s: output differs from the golden file; if intended, regenerate with UPDATE_GOLDEN=1 and review the diff", name)
		}
	}
}
