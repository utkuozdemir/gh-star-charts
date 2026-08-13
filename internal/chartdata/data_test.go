package chartdata_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
)

func day(s string) time.Time {
	t, err := time.Parse(chartdata.DateFormat, s)
	if err != nil {
		panic(err)
	}

	return t
}

func TestBuildPrefixAggregatesPerDay(t *testing.T) {
	pts := chartdata.BuildPrefix([]time.Time{
		day("2021-06-08"), day("2021-06-08"),
		day("2021-06-10"),
		day("2021-06-09"),
	})

	want := []chartdata.Point{
		{Date: "2021-06-08", Stars: 2},
		{Date: "2021-06-09", Stars: 3},
		{Date: "2021-06-10", Stars: 4},
	}

	if len(pts) != len(want) {
		t.Fatalf("got %d points, want %d", len(pts), len(want))
	}

	for i := range want {
		if pts[i] != want[i] {
			t.Errorf("point %d: got %+v, want %+v", i, pts[i], want[i])
		}
	}
}

func TestBuildPrefixEmpty(t *testing.T) {
	if pts := chartdata.BuildPrefix(nil); pts != nil {
		t.Fatalf("expected nil, got %+v", pts)
	}
}

func TestObserveSameDayLastWins(t *testing.T) {
	d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion}

	d.Observe(day("2026-08-13"), 100)
	d.Observe(day("2026-08-13"), 101)

	if len(d.Points) != 1 || d.Points[0].Stars != 101 {
		t.Fatalf("same-day observation must replace: %+v", d.Points)
	}

	if d.ObservedSince != "2026-08-13" || d.LastChecked != "2026-08-13" {
		t.Fatalf("boundary fields wrong: %+v", d)
	}
}

func TestObserveRecordsDecreases(t *testing.T) {
	d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion}

	d.Observe(day("2026-08-13"), 100)
	d.Observe(day("2026-08-14"), 80)

	if d.Points[1].Stars != 80 {
		t.Fatalf("unstar dip must be recorded: %+v", d.Points)
	}
}

func TestSetPrefixRefusesObservedOverlap(t *testing.T) {
	d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion}
	d.Observe(day("2026-08-13"), 100)

	err := d.SetPrefix([]chartdata.Point{{Date: "2026-08-13", Stars: 99}})
	if err == nil {
		t.Fatal("prefix reaching into observed points must be refused")
	}
}

func TestSetPrefixIsOnceOnly(t *testing.T) {
	d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion}
	d.Observe(day("2026-08-13"), 100)

	if err := d.SetPrefix([]chartdata.Point{{Date: "2026-08-10", Stars: 90}}); err != nil {
		t.Fatalf("first prefix install must work: %v", err)
	}

	if err := d.SetPrefix([]chartdata.Point{{Date: "2026-08-11", Stars: 95}}); err == nil {
		t.Fatal("second prefix install must be refused")
	}
}

func TestNewerSchemaRefusedOnLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	content := `{"schemaVersion": 999, "repoId": 1, "repo": "a/b", "points": []}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := chartdata.Load(path); !errors.Is(err, chartdata.ErrNewerSchema) {
		t.Fatalf("load must refuse newer schema, got %v", err)
	}

	d := &chartdata.Data{SchemaVersion: 999}
	if err := d.Save(path); !errors.Is(err, chartdata.ErrNewerSchema) {
		t.Fatalf("save must refuse newer schema, got %v", err)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "data.json")

	d := &chartdata.Data{
		SchemaVersion: chartdata.SchemaVersion,
		RepoID:        42, Repo: "a/b",
	}
	d.Observe(day("2026-08-13"), 10)

	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := chartdata.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.RepoID != 42 || len(got.Points) != 1 || got.Points[0].Stars != 10 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestObserveRefusesBackwardClock(t *testing.T) {
	d := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion}

	if err := d.Observe(day("2026-08-13"), 100); err != nil {
		t.Fatal(err)
	}

	if err := d.Observe(day("2023-01-01"), 5); err == nil {
		t.Fatal("an observation dated before the last one must be refused")
	}

	if len(d.Points) != 1 || d.ObservedSince != "2026-08-13" {
		t.Fatalf("refused observation must change nothing: %+v", d)
	}
}
