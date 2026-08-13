package cli

import (
	"os"
	"reflect"
	"testing"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
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
