// Package instance owns everything about the instance repo: the temp-clone
// git transaction, the generated caller workflow, the generated README, and
// the embed snippets.
//
// Concurrency model: every mutation clones fresh, applies a logical operation,
// commits, and pushes. On push rejection the tree is discarded and the
// operation replays against the new tip; generated files are never merged
// textually.
package instance

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/utkuozdemir/gh-star-charts/internal/manifest"
)

// WorkflowFile is the caller workflow's path inside the instance repo.
const WorkflowFile = "update-star-charts.yml"

// ProductRepo is where releases and docs live.
const ProductRepo = "utkuozdemir/gh-star-charts"

// PushAttempts bounds the replay loop.
const PushAttempts = 3

// Repo is a temp clone of the instance repo.
type Repo struct {
	// FullName is owner/name on GitHub.
	FullName string
	// Dir is the temp working tree.
	Dir string
	// Branch is the default branch (part of every embed URL).
	Branch string

	token string
}

// Clone makes a fresh shallow clone in a temp dir.
func Clone(fullName, token string) (*Repo, error) {
	dir, err := os.MkdirTemp("", "star-charts-*")
	if err != nil {
		return nil, err
	}

	r := &Repo{FullName: fullName, Dir: dir, token: token}

	out, err := r.git("clone", "--depth", "1", "https://github.com/"+fullName+".git", dir)
	if err != nil {
		os.RemoveAll(dir)

		return nil, fmt.Errorf("clone %s: %v: %s", fullName, err, out)
	}

	branch, err := r.git("-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		os.RemoveAll(dir)

		return nil, fmt.Errorf("resolve default branch: %v", err)
	}

	r.Branch = strings.TrimSpace(branch)

	return r, nil
}

// Close removes the working tree.
func (r *Repo) Close() { os.RemoveAll(r.Dir) }

// git runs git with the auth header passed through the environment, so the
// token appears neither in any config file nor in the process argument list,
// which other local processes can read. Signing is forced off: these are bot
// commits in a temp clone, and an inherited signing config would stall on
// hardware keys. The locale is pinned so error-message matching stays
// reliable, and terminal prompts are disabled so a bad token fails instead of
// hanging.
func (r *Repo) git(args ...string) (string, error) {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.token))
	full := append([]string{
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
	}, args...)

	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic,
		"GIT_AUTHOR_NAME=star-charts", "GIT_AUTHOR_EMAIL=star-charts@users.noreply.github.com",
		"GIT_COMMITTER_NAME=star-charts", "GIT_COMMITTER_EMAIL=star-charts@users.noreply.github.com",
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
	)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// CommitPush stages the given paths, commits, and pushes. It reports
// (pushed=false, err=nil) when there was nothing to commit, and
// ErrPushRejected when the remote moved.
func (r *Repo) CommitPush(message string, paths ...string) (bool, error) {
	args := append([]string{"-C", r.Dir, "add", "--"}, paths...)
	if out, err := r.git(args...); err != nil {
		return false, fmt.Errorf("git add: %v: %s", err, out)
	}

	if _, err := r.git("-C", r.Dir, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}

	if out, err := r.git("-C", r.Dir, "commit", "-m", message); err != nil {
		return false, fmt.Errorf("git commit: %v: %s", err, out)
	}

	if out, err := r.git("-C", r.Dir, "push", "origin", "HEAD:"+r.Branch); err != nil {
		if strings.Contains(out, "rejected") || strings.Contains(out, "fetch first") || strings.Contains(out, "non-fast-forward") {
			return false, ErrPushRejected
		}

		return false, fmt.Errorf("git push: %v: %s", err, out)
	}

	return true, nil
}

// ErrPushRejected signals the replay loop to re-clone and reapply.
var ErrPushRejected = fmt.Errorf("push rejected: remote moved")

// Transact runs op against a fresh clone, replaying on push rejection.
// op must be a pure function of the fresh tree: apply changes, then call
// CommitPush and return its error unchanged.
func Transact(fullName, token string, op func(*Repo) error) error {
	var lastErr error

	for attempt := 1; attempt <= PushAttempts; attempt++ {
		repo, err := Clone(fullName, token)
		if err != nil {
			return err
		}

		err = op(repo)
		repo.Close()

		if err == nil {
			return nil
		}

		lastErr = err

		if !errors.Is(err, ErrPushRejected) {
			return err
		}
	}

	return fmt.Errorf("push kept being rejected after %d attempts: %w", PushAttempts, lastErr)
}

// WorkflowYAML renders the self-contained caller workflow. version and sha256
// pin the exact binary; the inlined checksum is the trust anchor, and the
// workflow never fetches a checksum at run time. The schedule is fixed at
// daily: the data model keeps one point per UTC day, so a faster cron buys
// nothing and a slower one only makes the chart coarser.
func WorkflowYAML(version, sha256 string) string {
	asset := fmt.Sprintf("https://github.com/%s/releases/download/%s/gh-star-charts_%s_linux-amd64", ProductRepo, version, version)

	return fmt.Sprintf(`# Generated by gh-star-charts %[1]s. Re-run "gh star-charts init" to upgrade.
name: update-star-charts

on:
  schedule:
    - cron: "43 4 * * *"
  workflow_dispatch:

permissions:
  contents: write

concurrency:
  group: star-charts-update
  cancel-in-progress: false

jobs:
  update:
    runs-on: ubuntu-latest
    steps:
      - name: Download pinned gh-star-charts %[1]s
        run: |
          curl -fsSL --retry 3 -o /tmp/gh-star-charts "%[2]s"
          echo "%[3]s  /tmp/gh-star-charts" | sha256sum -c -
          chmod +x /tmp/gh-star-charts
      - name: Update charts
        env:
          GITHUB_TOKEN: ${{ github.token }}
        run: /tmp/gh-star-charts update --instance "${{ github.repository }}"
`, version, asset, sha256)
}

// EmbedSnippet is the copy-paste block for a chart, lint-safe and wrapped in
// a link to the repo's stargazers page.
func EmbedSnippet(instance *Repo, e manifest.Entry) string {
	base := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", instance.FullName, instance.Branch, e.Path)

	return fmt.Sprintf(`<!-- markdownlint-disable no-inline-html -->
<a href="https://github.com/%s/stargazers">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="%s/dark.svg" />
    <img alt="Star history of %s" src="%s/light.svg" />
  </picture>
</a>
<!-- markdownlint-enable no-inline-html -->`, e.Repo, base, e.Repo, base)
}

const (
	readmeBegin = "<!-- star-charts:begin -->"
	readmeEnd   = "<!-- star-charts:end -->"
)

// WriteReadme regenerates the instance README between the markers, creating
// the file when absent. Content outside the markers is preserved.
func WriteReadme(r *Repo, m *manifest.Manifest, version string) error {
	var b strings.Builder

	b.WriteString(readmeBegin + "\n")
	b.WriteString("# star-charts\n\n")
	fmt.Fprintf(&b, "Self-hosted GitHub star history charts, maintained by [gh-star-charts](https://github.com/%s) %s. A daily workflow in this repository updates the data and the images using only its own ephemeral `GITHUB_TOKEN`. No stored credentials, and no external service in the serving path.\n\n", ProductRepo, version)

	var active, paused []manifest.Entry

	for _, e := range m.Charts {
		if e.State == manifest.StateActive {
			active = append(active, e)
		} else {
			paused = append(paused, e)
		}
	}

	for _, e := range active {
		fmt.Fprintf(&b, "## %s\n\n", e.Repo)
		b.WriteString(EmbedSnippet(r, e) + "\n\n")
		b.WriteString("<details><summary>Embed this chart</summary>\n\n```html\n" + EmbedSnippet(r, e) + "\n```\n\n</details>\n\n")
	}

	if len(paused) > 0 {
		b.WriteString("## No longer updated\n\n")
		b.WriteString("These charts are paused. Their files and URLs keep serving the last recorded state.\n\n")

		for _, e := range paused {
			note := e.Note
			if note == "" {
				note = "paused"
			}

			fmt.Fprintf(&b, "- `%s` (%s), files at `%s/`.\n", e.Repo, note, e.Path)
		}

		b.WriteString("\n")
	}

	b.WriteString(readmeEnd + "\n")

	path := filepath.Join(r.Dir, "README.md")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := string(existing)

	if i, j := strings.Index(content, readmeBegin), strings.Index(content, readmeEnd); i >= 0 && j > i {
		content = content[:i] + b.String() + strings.TrimPrefix(content[j+len(readmeEnd):], "\n")
	} else {
		content = b.String()
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

// LooksLikeInstance reports whether an existing repo tree is safe to adopt:
// it has a manifest already, its README carries our markers, or it is a fresh
// auto-init repo (nothing but a trivial one-heading README and repo
// boilerplate). Anything else is somebody's real repository, and writing into
// it would destroy content.
func LooksLikeInstance(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, manifest.FileName)); err == nil {
		return true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, e := range entries {
		switch e.Name() {
		case ".git", "README.md", "LICENSE", ".gitignore":
		default:
			return false
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}

	if err != nil {
		return false
	}

	content := string(raw)
	if strings.Contains(content, readmeBegin) {
		return true
	}

	// The auto-init README is a heading plus at most a description line.
	trimmed := strings.TrimSpace(content)

	return strings.HasPrefix(trimmed, "# ") && len(trimmed) <= 300 && strings.Count(trimmed, "\n") <= 2
}

// ChartDir resolves a validated chart path inside the working tree and proves
// it cannot escape it, as a second line of defense in front of writes and
// especially recursive deletes.
func (r *Repo) ChartDir(chartPath string) (string, error) {
	if err := manifest.ValidateChartPath(chartPath); err != nil {
		return "", err
	}

	dir := filepath.Join(r.Dir, filepath.FromSlash(chartPath))

	rel, err := filepath.Rel(r.Dir, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("chart path %q escapes the repository", chartPath)
	}

	return dir, nil
}
