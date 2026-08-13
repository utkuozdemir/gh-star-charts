package instance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

func TestWorkflowYAML(t *testing.T) {
	wf := instance.WorkflowYAML("v1.2.3", strings.Repeat("a", 64))

	for _, want := range []string{
		"permissions:\n  contents: write",
		"workflow_dispatch:",
		"schedule:",
		"releases/download/v1.2.3/gh-star-charts_v1.2.3_linux-amd64",
		strings.Repeat("a", 64) + "  /tmp/gh-star-charts",
		"sha256sum -c -",
		`GITHUB_TOKEN: ${{ github.token }}`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow missing %q", want)
		}
	}

	if strings.Contains(wf, "actions: write") {
		t.Error("the workflow must hold contents: write and nothing else")
	}

	if strings.Contains(wf, "checkout") {
		t.Error("the workflow must not check out: update owns its own clone")
	}
}

func fakeRepo(t *testing.T) *instance.Repo {
	t.Helper()

	return &instance.Repo{FullName: "user/star-charts", Dir: t.TempDir(), Branch: "main"}
}

func TestWriteReadmePreservesOutsideMarkers(t *testing.T) {
	r := fakeRepo(t)
	path := filepath.Join(r.Dir, "README.md")

	before := "intro kept\n<!-- star-charts:begin -->\nold\n<!-- star-charts:end -->\noutro kept\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{SchemaVersion: manifest.SchemaVersion}
	if err := instance.WriteReadme(r, m, "v1.0.0"); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	content := string(raw)

	if !strings.HasPrefix(content, "intro kept\n") || !strings.HasSuffix(content, "outro kept\n") {
		t.Fatalf("content outside markers must survive:\n%s", content)
	}

	if strings.Contains(content, "old") {
		t.Fatal("content inside markers must be regenerated")
	}
}

func TestWriteReadmeMarkerAtEOFWithoutNewline(t *testing.T) {
	r := fakeRepo(t)
	path := filepath.Join(r.Dir, "README.md")

	// A user's editor stripped the trailing newline: this must not panic.
	before := "kept\n<!-- star-charts:begin -->\nx\n<!-- star-charts:end -->"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{SchemaVersion: manifest.SchemaVersion}
	if err := instance.WriteReadme(r, m, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestEmbedSnippet(t *testing.T) {
	r := fakeRepo(t)

	s := instance.EmbedSnippet(r, manifest.Entry{Repo: "owner/proj", Path: "charts/owner/proj"})

	for _, want := range []string{
		"https://github.com/owner/proj/stargazers",
		"https://raw.githubusercontent.com/user/star-charts/main/charts/owner/proj/dark.svg",
		"https://raw.githubusercontent.com/user/star-charts/main/charts/owner/proj/light.svg",
		"markdownlint-disable",
		"prefers-color-scheme: dark",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet missing %q", want)
		}
	}
}

func TestLooksLikeInstance(t *testing.T) {
	// Fresh auto-init repo: trivial README only.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# star-charts\nSelf-hosted charts\n"), 0o644)

	if !instance.LooksLikeInstance(dir) {
		t.Error("auto-init repo must be adoptable")
	}

	// A real repo with substantial README content must be refused.
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, "README.md"), []byte("# my project\n\nDocs.\n\nMore docs.\n\nEven more.\n"), 0o644)

	if instance.LooksLikeInstance(dir2) {
		t.Error("a repo with a real README must not be adoptable")
	}

	// A repo with source files must be refused regardless of README.
	dir3 := t.TempDir()
	os.WriteFile(filepath.Join(dir3, "main.go"), []byte("package main"), 0o644)

	if instance.LooksLikeInstance(dir3) {
		t.Error("a repo with code must not be adoptable")
	}

	// An existing instance (manifest present) is always adoptable.
	dir4 := t.TempDir()
	os.WriteFile(filepath.Join(dir4, manifest.FileName), []byte("schemaVersion: 2\ncharts: []\n"), 0o644)
	os.WriteFile(filepath.Join(dir4, "whatever.txt"), []byte("x"), 0o644)

	if !instance.LooksLikeInstance(dir4) {
		t.Error("a repo with a manifest must be adoptable")
	}
}

func TestChartDirContainment(t *testing.T) {
	r := fakeRepo(t)

	if _, err := r.ChartDir("charts/owner/repo"); err != nil {
		t.Fatalf("valid path must resolve: %v", err)
	}

	for _, p := range []string{"charts/../..", "..", "charts/../../../tmp"} {
		if _, err := r.ChartDir(p); err == nil {
			t.Errorf("%q must not resolve", p)
		}
	}
}
