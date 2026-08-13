// Package chartdata owns the per-chart data file: the versioned envelope around
// the daily star-count points, and the merge rules that protect observed history.
package chartdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SchemaVersion is the current data.json schema. A binary must refuse to write
// a file whose version is newer than this.
const SchemaVersion = 1

// DateFormat is the canonical UTC day format used everywhere in the data file.
const DateFormat = "2006-01-02"

// Point is one UTC day's cumulative star count.
type Point struct {
	Date  string `json:"date"`
	Stars int    `json:"stars"`
}

// Data is the envelope persisted as data.json.
type Data struct {
	SchemaVersion int    `json:"schemaVersion"`
	RepoID        int64  `json:"repoId"`
	Repo          string `json:"repo"`
	// ObservedSince is the first date with an authoritative sampled total.
	// Points before it are survivor reconstruction from starred_at timestamps.
	ObservedSince string `json:"observedSince"`
	// PrefixTruncated records that backfill hit the pagination cap, so no
	// reconstructed prefix exists and the chart starts at ObservedSince.
	PrefixTruncated bool `json:"prefixTruncated"`
	// LastChecked advances only when this repo's metadata was fetched
	// successfully with a matching repository ID.
	LastChecked string  `json:"lastChecked"`
	Points      []Point `json:"points"`
}

// ErrNewerSchema is returned when a data file was written by a newer binary.
var ErrNewerSchema = errors.New("data file has a newer schema version than this binary supports")

// Load reads and validates a data file.
func Load(path string) (*Data, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if d.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%s: %w (file v%d, binary v%d): upgrade the extension", path, ErrNewerSchema, d.SchemaVersion, SchemaVersion)
	}

	return &d, nil
}

// Save writes the data file atomically (temp file + rename).
func (d *Data) Save(path string) error {
	if d.SchemaVersion > SchemaVersion {
		return ErrNewerSchema
	}

	sort.Slice(d.Points, func(i, j int) bool { return d.Points[i].Date < d.Points[j].Date })

	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".data-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// BuildPrefix aggregates starred_at timestamps into daily cumulative points.
// It is only ever called on first add (or explicit reset): the merge rule is
// that a reconstructed prefix may never overwrite observed points.
func BuildPrefix(starredAt []time.Time) []Point {
	if len(starredAt) == 0 {
		return nil
	}

	perDay := map[string]int{}
	for _, t := range starredAt {
		perDay[t.UTC().Format(DateFormat)]++
	}

	days := make([]string, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}

	sort.Strings(days)

	points := make([]Point, 0, len(days))
	total := 0

	for _, d := range days {
		total += perDay[d]
		points = append(points, Point{Date: d, Stars: total})
	}

	return points
}

// Observe records an authoritative sampled total for the given UTC day,
// replacing an existing same-day point (last observation wins) and advancing
// LastChecked. It never touches any other point, and it refuses observations
// dated before what was already observed: a machine with a wrong clock must
// not overwrite reconstructed history or reclassify it as observed.
func (d *Data) Observe(day time.Time, stars int) error {
	date := day.UTC().Format(DateFormat)

	if d.LastChecked != "" && date < d.LastChecked {
		return fmt.Errorf("observation date %s is before the last observation %s, refusing (clock skew?)", date, d.LastChecked)
	}

	if d.ObservedSince == "" {
		d.ObservedSince = date
	}

	if date > d.LastChecked {
		d.LastChecked = date
	}

	for i := range d.Points {
		if d.Points[i].Date == date {
			d.Points[i].Stars = stars

			return nil
		}
	}

	d.Points = append(d.Points, Point{Date: date, Stars: stars})
	sort.Slice(d.Points, func(i, j int) bool { return d.Points[i].Date < d.Points[j].Date })

	return nil
}

// SetPrefix installs a reconstructed prefix. It fails if observed points exist
// and the prefix would reach into them, or if a prefix is already present
// (prefix backfill is once-only; reset clears the file first).
func (d *Data) SetPrefix(points []Point) error {
	if d.ObservedSince != "" {
		for _, p := range points {
			if p.Date >= d.ObservedSince {
				return fmt.Errorf("prefix point %s is not strictly before observedSince %s", p.Date, d.ObservedSince)
			}
		}
	}

	for _, p := range d.Points {
		if d.ObservedSince == "" || p.Date < d.ObservedSince {
			return errors.New("a reconstructed prefix already exists, use reset to rebuild it")
		}
	}

	d.Points = append(points, d.Points...)
	sort.Slice(d.Points, func(i, j int) bool { return d.Points[i].Date < d.Points[j].Date })

	return nil
}
