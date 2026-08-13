package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

func TestValidateChartPath(t *testing.T) {
	valid := []string{
		"charts/owner/repo",
		"charts/owner/repo.with-dots_and-dashes",
		"charts/deep/nested/path",
	}

	for _, p := range valid {
		if err := manifest.ValidateChartPath(p); err != nil {
			t.Errorf("%q must be valid: %v", p, err)
		}
	}

	invalid := []string{
		"..",
		"charts/..",
		"charts/../escape",
		"charts/./x",
		"charts//x",
		"/etc/passwd",
		"charts/owner/../../..",
		"elsewhere/owner/repo",
		"charts",
		"charts/OWNER/REPO",
		`charts/owner/re"po`,
		"charts/owner/re po",
	}

	for _, p := range invalid {
		if err := manifest.ValidateChartPath(p); err == nil {
			t.Errorf("%q must be rejected", p)
		}
	}
}

func TestLoadRejectsInvalidPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifest.FileName)

	content := `schemaVersion: 2
charts:
  - repoId: 1
    repo: a/b
    path: charts/../escape
    state: active
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := manifest.Load(path); err == nil || !strings.Contains(err.Error(), "invalid segment") {
		t.Fatalf("hostile path in a manifest must be rejected, got %v", err)
	}
}

func TestSaveMigratesSchemaForward(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifest.FileName)

	content := `schemaVersion: 1
charts:
  - repoId: 1
    repo: a/b
    path: charts/a/b
    state: active
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "schemaVersion: 2") {
		t.Fatalf("save must stamp the current schema, got:\n%s", raw)
	}
}

func TestLoadRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifest.FileName)

	if err := os.WriteFile(path, []byte("schemaVersion: 999\ncharts: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := manifest.Load(path); err == nil {
		t.Fatal("newer schema must be refused")
	}
}

func TestRoundtripWithStyle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifest.FileName)

	m := &manifest.Manifest{SchemaVersion: manifest.SchemaVersion}
	m.Charts = append(m.Charts, manifest.Entry{
		RepoID: 42, Repo: "a/b", Path: "charts/a/b", State: manifest.StateActive,
		Style: manifest.Style{LineColor: "#123456", Look: "clean"},
	})

	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	e := got.FindByID(42)
	if e == nil || e.Style.LineColor != "#123456" || e.Style.Look != "clean" {
		t.Fatalf("style did not survive the roundtrip: %+v", e)
	}
}

func TestFindByPathIsCaseInsensitive(t *testing.T) {
	m := &manifest.Manifest{SchemaVersion: manifest.SchemaVersion}
	m.Charts = append(m.Charts, manifest.Entry{RepoID: 1, Repo: "a/b", Path: "charts/a/b", State: manifest.StateActive})

	if m.FindByPath("charts/A/B") == nil {
		t.Fatal("path lookup must be case-insensitive to prevent case-collision dirs")
	}
}
