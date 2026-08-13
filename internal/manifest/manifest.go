// Package manifest owns star-charts.yaml, the instance repo's tracked set.
// The extension is the sole writer; hand edits are supported only for pausing,
// unpausing, and removing entries.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current manifest schema.
const SchemaVersion = 1

// FileName is the manifest's fixed location in the instance repo root.
const FileName = "star-charts.yaml"

// States an entry can be in.
const (
	StateActive = "active"
	StatePaused = "paused"
)

// Entry is one tracked repository.
type Entry struct {
	// RepoID pins identity: it detects a tracked name being reused by a
	// different repository after a rename.
	RepoID int64 `yaml:"repoId"`
	// Repo is the canonical owner/name, updated after renames.
	Repo string `yaml:"repo"`
	// Path is the chart directory, frozen at add time (URLs are forever).
	Path  string `yaml:"path"`
	State string `yaml:"state"`
	// ConsecutiveFailures counts back-to-back permanent-shape failures;
	// reaching the auto-pause threshold flips State to paused.
	ConsecutiveFailures int `yaml:"consecutiveFailures,omitempty"`
	// Note documents why an entry was auto-paused.
	Note string `yaml:"note,omitempty"`
	// Style holds per-chart appearance overrides.
	Style Style `yaml:"style,omitempty"`
}

// Style are optional per-chart appearance overrides; empty fields keep the
// validated defaults. A single value applies to both modes unless the dark
// variant is set.
type Style struct {
	LineColor      string `yaml:"lineColor,omitempty"`
	LineColorDark  string `yaml:"lineColorDark,omitempty"`
	Background     string `yaml:"background,omitempty"`
	BackgroundDark string `yaml:"backgroundDark,omitempty"`
	// Look selects the chart style: empty or "sketchy" is the classic
	// hand-drawn default, "clean" is the plain alternative.
	Look string `yaml:"look,omitempty"`
}

// IsZero reports whether no override is set (also used by yaml omitempty).
func (s Style) IsZero() bool {
	return s == Style{}
}

// Manifest is the file's root.
type Manifest struct {
	SchemaVersion int `yaml:"schemaVersion"`
	// Cron overrides the update schedule; empty means the default daily run.
	Cron   string  `yaml:"cron,omitempty"`
	Charts []Entry `yaml:"charts"`
}

// AutoPauseThreshold is how many consecutive permanent-shape failures flip an
// entry to paused.
const AutoPauseThreshold = 3

// ErrNewerSchema mirrors chartdata's rule: never write what we cannot read.
var ErrNewerSchema = errors.New("manifest has a newer schema version than this binary supports")

// Load reads the manifest, tolerating an absent file (empty manifest).
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Manifest{SchemaVersion: SchemaVersion}, nil
	}

	if err != nil {
		return nil, err
	}

	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if m.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%s: %w (file v%d, binary v%d)", path, ErrNewerSchema, m.SchemaVersion, SchemaVersion)
	}

	for i := range m.Charts {
		e := &m.Charts[i]
		if e.Repo == "" || e.Path == "" || e.RepoID == 0 {
			return nil, fmt.Errorf("%s: entry %d is missing repoId, repo, or path; entries are managed by the extension, hand edits should only pause, unpause, or remove", path, i)
		}

		if e.State != StateActive && e.State != StatePaused {
			return nil, fmt.Errorf("%s: entry %q has unknown state %q", path, e.Repo, e.State)
		}
	}

	return &m, nil
}

// Save writes the manifest deterministically.
func (m *Manifest) Save(path string) error {
	if m.SchemaVersion > SchemaVersion {
		return ErrNewerSchema
	}

	sort.Slice(m.Charts, func(i, j int) bool { return m.Charts[i].Repo < m.Charts[j].Repo })

	raw, err := yaml.Marshal(m)
	if err != nil {
		return err
	}

	return os.WriteFile(path, raw, 0o644)
}

// FindByID returns the entry pinned to the given repository ID, or nil.
func (m *Manifest) FindByID(id int64) *Entry {
	for i := range m.Charts {
		if m.Charts[i].RepoID == id {
			return &m.Charts[i]
		}
	}

	return nil
}

// FindByPath returns the entry occupying the given chart path, or nil.
func (m *Manifest) FindByPath(path string) *Entry {
	for i := range m.Charts {
		if strings.EqualFold(m.Charts[i].Path, path) {
			return &m.Charts[i]
		}
	}

	return nil
}

// ChartPath derives the default chart directory for a repo name: nested,
// lowercased, frozen at add time.
func ChartPath(fullName string) string {
	return "charts/" + strings.ToLower(fullName)
}
