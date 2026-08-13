package ghapi_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/ghapi"
)

// fakeStargazers serves paginated starred_at entries for total stars.
func fakeStargazers(t *testing.T, total int) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))

		start := (page - 1) * perPage
		end := start + perPage

		if end > total {
			end = total
		}

		if start > total {
			start = total
		}

		fmt.Fprint(w, "[")

		for i := start; i < end; i++ {
			if i > start {
				fmt.Fprint(w, ",")
			}

			ts := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
			fmt.Fprintf(w, `{"starred_at":%q}`, ts.Format(time.RFC3339))
		}

		fmt.Fprint(w, "]")
	}))
}

func TestBackfillComplete(t *testing.T) {
	srv := fakeStargazers(t, 250)
	defer srv.Close()

	c := ghapi.NewWithBaseURL(srv.URL, "t")

	res, err := c.Backfill("a/b", 250)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.StarredAt) != 250 || res.Truncated {
		t.Fatalf("got %d stars, truncated=%v", len(res.StarredAt), res.Truncated)
	}
}

func TestBackfillExactlyAtCapIsNotTruncated(t *testing.T) {
	total := ghapi.PageCap * 100

	srv := fakeStargazers(t, total)
	defer srv.Close()

	c := ghapi.NewWithBaseURL(srv.URL, "t")

	res, err := c.Backfill("a/b", total)
	if err != nil {
		t.Fatal(err)
	}

	if res.Truncated {
		t.Fatal("a repo with exactly the cap's worth of stars is complete, not truncated")
	}

	if len(res.StarredAt) != total {
		t.Fatalf("got %d stars, want %d", len(res.StarredAt), total)
	}
}

func TestBackfillBeyondCapIsTruncated(t *testing.T) {
	total := ghapi.PageCap*100 + 5000

	srv := fakeStargazers(t, total)
	defer srv.Close()

	c := ghapi.NewWithBaseURL(srv.URL, "t")

	res, err := c.Backfill("a/b", total)
	if err != nil {
		t.Fatal(err)
	}

	if !res.Truncated {
		t.Fatal("stars beyond the cap must mark the result truncated")
	}
}

func TestStatusErrorShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/a/gone":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case "/repos/a/forbidden":
			w.Header().Set("X-Accepted-Github-Permissions", "metadata=read; contents=write")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible"}`)
		case "/repos/a/limited":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
		}
	}))
	defer srv.Close()

	c := ghapi.NewWithBaseURL(srv.URL, "t")

	_, err := c.GetRepo("a/gone")

	var se *ghapi.StatusError
	if !errors.As(err, &se) || !se.IsPermanent() {
		t.Fatalf("404 must be permanent, got %v", err)
	}

	_, err = c.GetRepo("a/forbidden")
	if !errors.As(err, &se) || !se.IsPermanent() || se.AcceptedPermissions == "" {
		t.Fatalf("403 must be permanent and carry accepted permissions, got %v", err)
	}

	_, err = c.GetRepo("a/limited")
	if !errors.As(err, &se) || se.IsPermanent() {
		t.Fatalf("rate limiting must not be permanent, got %v", err)
	}
}
