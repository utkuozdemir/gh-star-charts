// Package ghapi is a minimal GitHub REST client covering exactly what the tool
// needs: repo metadata, stargazer timestamps, repo creation, and workflow
// re-enabling. Auth comes from the environment (GH_TOKEN / GITHUB_TOKEN) or,
// interactively, from `gh auth token`.
package ghapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	apiRoot = "https://api.github.com"

	// PageCap is GitHub's REST pagination limit on the stargazers endpoint:
	// pages past 400 (at 100 per page) are not served.
	PageCap = 400

	perPage = 100
)

// Repo is the subset of repository metadata the tool uses.
type Repo struct {
	ID              int64  `json:"id"`
	FullName        string `json:"full_name"`
	Private         bool   `json:"private"`
	StargazersCount int    `json:"stargazers_count"`
}

// Client is a token-holding GitHub API client. The token lives in memory only.
type Client struct {
	token   string
	http    *http.Client
	baseURL string
}

// ErrNoAuth is returned when no usable token can be resolved.
var ErrNoAuth = errors.New("no GitHub credentials: set GH_TOKEN/GITHUB_TOKEN or log in with `gh auth login`")

// StatusError carries the HTTP status of a failed API call so callers can
// distinguish permanent shapes (404, 403) from transient ones (429, 5xx).
type StatusError struct {
	StatusCode int
	Message    string
	// AcceptedPermissions echoes x-accepted-github-permissions on 403s.
	AcceptedPermissions string
	// SSO is set when the 403 asked for single-sign-on authorization.
	SSO bool
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github api: HTTP %d: %s", e.StatusCode, e.Message)
}

// IsPermanent reports whether the failure will not resolve by retrying:
// the repo is gone, access is denied, or it moved out of reach.
func (e *StatusError) IsPermanent() bool {
	return e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusGone ||
		(e.StatusCode == http.StatusForbidden && !strings.Contains(strings.ToLower(e.Message), "rate limit"))
}

// New resolves credentials and returns a client. Environment wins; the gh CLI
// keychain is the interactive fallback.
func New() (*Client, error) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(env)); t != "" {
			return &Client{token: t, http: &http.Client{Timeout: 30 * time.Second}, baseURL: apiRoot}, nil
		}
	}

	out, err := exec.Command("gh", "auth", "token", "--hostname", "github.com").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return &Client{token: t, http: &http.Client{Timeout: 30 * time.Second}, baseURL: apiRoot}, nil
		}
	}

	return nil, ErrNoAuth
}

// NewWithBaseURL builds a client against an arbitrary API root, which is what
// lets tests run against a local fake server.
func NewWithBaseURL(baseURL, token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 30 * time.Second}, baseURL: baseURL}
}

// Token exposes the resolved token for git transport auth. It is never logged
// or persisted.
func (c *Client) Token() string { return c.token }

func (c *Client) do(method, path string, accept string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()

		msg := struct {
			Message string `json:"message"`
		}{}

		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = json.Unmarshal(raw, &msg)

		return nil, &StatusError{
			StatusCode:          resp.StatusCode,
			Message:             msg.Message,
			AcceptedPermissions: resp.Header.Get("X-Accepted-Github-Permissions"),
			SSO:                 resp.Header.Get("X-Github-Sso") != "",
		}
	}

	return resp, nil
}

func (c *Client) getJSON(path string, accept string, out any) error {
	resp, err := c.do(http.MethodGet, path, accept, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(out)
}

// GetRepo fetches repository metadata, following GitHub's rename redirects.
func (c *Client) GetRepo(fullName string) (*Repo, error) {
	var r Repo
	if err := c.getJSON("/repos/"+fullName, "", &r); err != nil {
		return nil, err
	}

	return &r, nil
}

// AuthenticatedLogin returns the login of the token's user.
func (c *Client) AuthenticatedLogin() (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.getJSON("/user", "", &u); err != nil {
		return "", err
	}

	return u.Login, nil
}

// AuthenticatedHost reports which host the tool will talk to. The token is
// always resolved for github.com specifically, so a GHES login being active
// cannot leak a GHES token to api.github.com; the only rejectable state is an
// explicit GH_HOST override.
func AuthenticatedHost() string {
	if h := strings.TrimSpace(os.Getenv("GH_HOST")); h != "" {
		return h
	}

	return "github.com"
}

// BackfillResult is the outcome of a stargazer-timestamp backfill.
type BackfillResult struct {
	StarredAt []time.Time
	// Truncated is set when the pagination cap was hit with more stars
	// remaining, meaning no honest reconstructed prefix exists.
	Truncated bool
}

// Backfill pages through the stargazer timestamps. The caller must hold
// write access on the repo (GitHub's post-June-2026 restriction). progress,
// when non-nil, is called after every fetched page with the number of
// timestamps collected so far, so the CLI can report on long backfills.
func (c *Client) Backfill(fullName string, totalStars int, progress func(fetched int)) (*BackfillResult, error) {
	res := &BackfillResult{}

	for page := 1; ; page++ {
		if page > PageCap {
			// The cap only truncates when stars actually remain beyond it: a
			// repo of exactly PageCap*100 stars is complete, not truncated.
			res.Truncated = len(res.StarredAt) < totalStars

			return res, nil
		}

		var batch []struct {
			StarredAt time.Time `json:"starred_at"`
		}

		path := fmt.Sprintf("/repos/%s/stargazers?per_page=%d&page=%d", fullName, perPage, page)
		if err := c.getJSON(path, "application/vnd.github.star+json", &batch); err != nil {
			return nil, fmt.Errorf("backfill page %d: %w", page, err)
		}

		for _, s := range batch {
			res.StarredAt = append(res.StarredAt, s.StarredAt)
		}

		if progress != nil {
			progress(len(res.StarredAt))
		}

		if len(batch) < perPage {
			return res, nil
		}
	}
}

// CreateUserRepo creates a public repository under the authenticated user.
func (c *Client) CreateUserRepo(name, description string) (*Repo, error) {
	payload, err := json.Marshal(map[string]any{
		"name":        name,
		"description": description,
		"private":     false,
		"has_wiki":    false,
		"has_issues":  true,
		"auto_init":   true,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.do(http.MethodPost, "/user/repos", "", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r Repo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}

	return &r, nil
}

// EnableWorkflow re-enables a workflow by file name, clearing a 60-day
// auto-disable. Called from init/add under the user's own auth.
func (c *Client) EnableWorkflow(instanceRepo, workflowFile string) error {
	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/enable", instanceRepo, url.PathEscape(workflowFile))

	resp, err := c.do(http.MethodPut, path, "", nil)
	if err != nil {
		return err
	}

	return resp.Body.Close()
}
