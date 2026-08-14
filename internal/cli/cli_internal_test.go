package cli

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
	"github.com/utkuozdemir/gh-star-charts/internal/instance"
	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

// The docs show flags after positionals, so parsing must accept both orders.
func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{
			in:   []string{"owner/repo", "--line-color", "#e86161"},
			want: []string{"--line-color", "#e86161", "owner/repo"},
		},
		{
			in:   []string{"--line-color", "#e86161", "owner/repo"},
			want: []string{"--line-color", "#e86161", "owner/repo"},
		},
		{
			in:   []string{"owner/repo", "--purge"},
			want: []string{"--purge", "owner/repo"},
		},
		{
			in:   []string{"a/b", "--look=clean", "c/d"},
			want: []string{"--look=clean", "a/b", "c/d"},
		},
		{
			in:   []string{"a/b", "--yes"},
			want: []string{"--yes", "a/b"},
		},
	}

	for _, c := range cases {
		if got := normalizeArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("normalizeArgs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Asking for help must be a successful operation, not an error exit. These
// paths return before any network or auth work, so they are safe to drive
// through the real dispatcher.
func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{
		{"help"},
		{"--help"},
		{"-h"},
		{"add", "-h"},
		{"init", "--help"},
		{"list", "-h"},
		{"update", "-h"},
		{"help", "add"},
	} {
		if code := Run(args); code != 0 {
			t.Errorf("Run(%v) = %d, want 0", args, code)
		}
	}

	// An unknown command is still an error.
	if code := Run([]string{"bogus"}); code != 2 {
		t.Errorf("Run(bogus) = %d, want 2", code)
	}
}

// Help output must go to stdout so it can be piped, while parse errors stay
// on stderr.
func TestParseFlagsRoutesHelpToStdout(t *testing.T) {
	fs := newFlagSet("x", "x [flags]")
	if err := parseFlags(fs, []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("want ErrHelp, got %v", err)
	}

	if fs.Output() != os.Stdout {
		t.Error("help must be routed to stdout")
	}

	fs = newFlagSet("y", "y [flags]")
	if err := parseFlags(fs, []string{"a/b"}); err != nil {
		t.Fatalf("plain args must parse: %v", err)
	}

	if fs.Output() == os.Stdout {
		t.Error("without a help request, output must stay on stderr")
	}
}

// list rows degrade honestly: a data file that is missing, unreadable, or
// pinned to a different repository must not contribute stars or freshness.
func TestListRow(t *testing.T) {
	r := &instance.Repo{FullName: "user/star-charts", Dir: t.TempDir(), Branch: "main"}

	writeData := func(t *testing.T, path string, d *chartdata.Data) {
		t.Helper()

		dir := filepath.Join(r.Dir, filepath.FromSlash(path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := d.Save(filepath.Join(dir, "data.json")); err != nil {
			t.Fatal(err)
		}
	}

	healthy := &chartdata.Data{SchemaVersion: chartdata.SchemaVersion, RepoID: 1, Repo: "o/a", LastChecked: "2026-08-14", Points: []chartdata.Point{{Date: "2026-08-14", Stars: 42}}}
	writeData(t, "charts/o/a", healthy)

	state, stars, lastChecked, detail := listRow(r, manifest.Entry{RepoID: 1, Repo: "o/a", Path: "charts/o/a", State: manifest.StateActive})
	if state != "active" || stars != "42" || lastChecked != "2026-08-14" || detail != "" {
		t.Errorf("healthy row wrong: %q %q %q %q", state, stars, lastChecked, detail)
	}

	// Missing data file.
	_, stars, lastChecked, detail = listRow(r, manifest.Entry{RepoID: 2, Repo: "o/b", Path: "charts/o/b", State: manifest.StateActive})
	if stars != "-" || lastChecked != "-" || !strings.Contains(detail, "missing or unreadable") {
		t.Errorf("missing-data row wrong: %q %q %q", stars, lastChecked, detail)
	}

	// A data file pinned to another repository: its numbers must not appear.
	writeData(t, "charts/o/c", healthy)

	_, stars, lastChecked, detail = listRow(r, manifest.Entry{RepoID: 3, Repo: "o/c", Path: "charts/o/c", State: manifest.StateActive})
	if stars != "-" || lastChecked != "-" || !strings.Contains(detail, "belongs to repo id 1") {
		t.Errorf("id-mismatch row wrong: %q %q %q", stars, lastChecked, detail)
	}

	// Failing entries show the counter, and a data note appends to it.
	state, _, _, detail = listRow(r, manifest.Entry{RepoID: 2, Repo: "o/b", Path: "charts/o/b", State: manifest.StateActive, ConsecutiveFailures: 2})
	if state != "failing" || !strings.Contains(detail, "2/3 failures") || !strings.Contains(detail, "missing or unreadable") {
		t.Errorf("failing row wrong: %q %q", state, detail)
	}

	// Paused entries keep their note.
	state, _, _, detail = listRow(r, manifest.Entry{RepoID: 1, Repo: "o/a", Path: "charts/o/a", State: manifest.StatePaused, Note: "removed by owner"})
	if state != "paused" || detail != "removed by owner" {
		t.Errorf("paused row wrong: %q %q", state, detail)
	}
}

// A hand-edited daily run time must survive workflow regeneration, and
// anything that changes the cadence must be restored to the default.
func TestWriteWorkflowPreservesDailyCron(t *testing.T) {
	sum := strings.Repeat("a", 64)
	r := &instance.Repo{FullName: "user/star-charts", Dir: t.TempDir(), Branch: "main"}
	wfPath := filepath.Join(r.Dir, ".github", "workflows", instance.WorkflowFile)

	// Fresh instance: the default schedule.
	if err := writeWorkflow(r, "v1.0.0", sum); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(wfPath)
	if !strings.Contains(string(raw), `cron: "`+instance.DefaultCron+`"`) {
		t.Fatalf("fresh workflow must carry the default cron:\n%s", raw)
	}

	// The user edits the run time, then an upgrade regenerates the file.
	edited := strings.Replace(string(raw), instance.DefaultCron, "7 21 * * *", 1)
	os.WriteFile(wfPath, []byte(edited), 0o644)

	if err := writeWorkflow(r, "v1.1.0", sum); err != nil {
		t.Fatal(err)
	}

	raw, _ = os.ReadFile(wfPath)
	if !strings.Contains(string(raw), `cron: "7 21 * * *"`) {
		t.Fatalf("edited daily run time must survive regeneration:\n%s", raw)
	}

	if !strings.Contains(string(raw), "v1.1.0") {
		t.Fatal("the pin must still be upgraded")
	}

	// A cadence change does not survive: the default comes back.
	edited = strings.Replace(string(raw), "7 21 * * *", "*/5 * * * *", 1)
	os.WriteFile(wfPath, []byte(edited), 0o644)

	if err := writeWorkflow(r, "v1.2.0", sum); err != nil {
		t.Fatal(err)
	}

	raw, _ = os.ReadFile(wfPath)
	if !strings.Contains(string(raw), `cron: "`+instance.DefaultCron+`"`) {
		t.Fatalf("a non-daily schedule must be restored to the default:\n%s", raw)
	}
}

func TestIsPermanentFailure(t *testing.T) {
	permanent := []error{
		&ghapi.StatusError{StatusCode: 404},
		&ghapi.StatusError{StatusCode: 403, Message: "forbidden"},
		os.ErrNotExist,
		chartdata.ErrNewerSchema,
		manifest.ErrNewerSchema,
	}

	for _, err := range permanent {
		if !isPermanentFailure(err) {
			t.Errorf("%v must classify permanent", err)
		}
	}

	transient := []error{
		&ghapi.StatusError{StatusCode: 403, Message: "API rate limit exceeded"},
		&ghapi.StatusError{StatusCode: 500},
		&ghapi.StatusError{StatusCode: 429},
		os.ErrDeadlineExceeded,
	}

	for _, err := range transient {
		if isPermanentFailure(err) {
			t.Errorf("%v must classify transient", err)
		}
	}
}

func TestValidStyleRejectsInjection(t *testing.T) {
	bad := manifest.Style{LineColor: `"><script>`}
	if err := validStyle(bad); err == nil {
		t.Fatal("attribute injection must be rejected")
	}

	good := manifest.Style{LineColor: "#e86161", LineColorDark: "rgb(1, 2, 3)"}
	if err := validStyle(good); err != nil {
		t.Fatalf("normal colors must pass: %v", err)
	}
}

func TestThemesForDefaultsToSketchy(t *testing.T) {
	themes := themesFor(manifest.Style{})
	if !themes[0].Sketchy || !themes[1].Sketchy {
		t.Fatal("the hand-drawn look must be the default")
	}

	themes = themesFor(manifest.Style{Look: "clean"})
	if themes[0].Sketchy || themes[1].Sketchy {
		t.Fatal("look=clean must opt out of the hand-drawn look")
	}
}
