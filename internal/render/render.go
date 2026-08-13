// Package render turns chart data into the light and dark SVG files.
// Hand-written SVG, no chart library, deterministic output.
//
// Layout rules (see the repo docs for the reasoning): text is anchored so
// overflow is harmless, nothing is sized to fit a measured string, text wears
// ink colors while only marks wear the series color, and no web fonts exist
// because GitHub's image proxy forbids external fetches.
package render

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
)

// Theme holds the palette for one mode. Colors validated against GitHub's
// README surfaces (light #ffffff, dark #0d1117).
type Theme struct {
	Name      string
	Line      string
	AreaOp    string
	Primary   string
	Secondary string
	Muted     string
	Grid      string
	Axis      string
	// Background paints an explicit chart background; empty stays
	// transparent so the chart blends into the page.
	Background string
}

// WithOverrides returns a copy of the theme with per-chart style applied.
// Empty overrides keep the defaults.
func (t Theme) WithOverrides(line, background string) Theme {
	if line != "" {
		t.Line = line
	}

	if background != "" {
		t.Background = background
	}

	return t
}

// Light and Dark are the two shipped themes.
var (
	Light = Theme{
		Name: "light", Line: "#2a78d6", AreaOp: "0.10",
		Primary: "#1f2328", Secondary: "#59636e", Muted: "#818b98",
		Grid: "#eaeef2", Axis: "#d1d9e0",
	}
	Dark = Theme{
		Name: "dark", Line: "#3987e5", AreaOp: "0.14",
		Primary: "#f0f6fc", Secondary: "#9198a1", Muted: "#767e89",
		Grid: "#21262d", Axis: "#3d444d",
	}
)

const (
	width   = 800
	height  = 340
	marginL = 64
	marginR = 24
	marginT = 56
	marginB = 52
)

// SVG renders one theme of the chart.
func SVG(d *chartdata.Data, th Theme) string {
	pts := d.Points

	// Extend the curve to lastChecked with a flat segment, so the right edge
	// of the line is a freshness indicator by construction.
	if n := len(pts); n > 0 && d.LastChecked > pts[n-1].Date {
		pts = append(append([]chartdata.Point{}, pts...), chartdata.Point{Date: d.LastChecked, Stars: pts[n-1].Stars})
	}

	var b strings.Builder

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="Star history of %s">`+"\n",
		width, height, width, height, esc(d.Repo))
	b.WriteString(`<style>text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}</style>` + "\n")

	if th.Background != "" {
		fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" rx="6" fill="%s"/>`+"\n", width, height, th.Background)
	}

	// Title and current count, ink colors only.
	fmt.Fprintf(&b, `<text x="%d" y="28" font-size="15" font-weight="600" fill="%s">%s</text>`+"\n", marginL, th.Primary, esc(d.Repo))

	current := 0
	if len(pts) > 0 {
		current = pts[len(pts)-1].Stars
	}

	fmt.Fprintf(&b, `<text x="%d" y="28" font-size="14" text-anchor="end" fill="%s">&#9733; %s</text>`+"\n", width-marginR, th.Secondary, group(current))

	if len(pts) == 0 {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="13" text-anchor="middle" fill="%s">no data yet</text>`+"\n", width/2, height/2, th.Muted)
		b.WriteString(`</svg>` + "\n")

		return b.String()
	}

	x0, x1 := dayNum(pts[0].Date), dayNum(pts[len(pts)-1].Date)
	if x1 == x0 {
		x1 = x0 + 1
	}

	yMax, ySegments := niceCeil(maxStars(pts))

	xOf := func(day int) float64 {
		return marginL + float64(day-x0)/float64(x1-x0)*(width-marginL-marginR)
	}
	yOf := func(stars int) float64 {
		return float64(height-marginB) - float64(stars)/float64(yMax)*(height-marginT-marginB)
	}

	// Recessive horizontal gridlines with y labels.
	for s := 0; s <= ySegments; s++ {
		v := yMax / ySegments * s
		y := yOf(v)

		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1"/>`+"\n", marginL, y, width-marginR, y, th.Grid)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="12" text-anchor="end" fill="%s">%s</text>`+"\n", marginL-8, y+4, th.Muted, group(v))
	}

	// X tick labels: years for long spans, months otherwise.
	for _, tick := range xTicks(pts[0].Date, pts[len(pts)-1].Date) {
		x := xOf(dayNum(tick.day))
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1"/>`+"\n", x, marginT, x, height-marginB, th.Grid)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="12" text-anchor="middle" fill="%s">%s</text>`+"\n", x, height-marginB+20, th.Muted, tick.label)
	}

	// Baseline.
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="1"/>`+"\n",
		marginL, height-marginB, width-marginR, height-marginB, th.Axis)

	// Area fill and line, the only elements wearing the series color.
	line := make([]string, 0, len(pts))
	for _, p := range pts {
		line = append(line, fmt.Sprintf("%.1f,%.1f", xOf(dayNum(p.Date)), yOf(p.Stars)))
	}

	area := fmt.Sprintf("%.1f,%d %s %.1f,%d", xOf(dayNum(pts[0].Date)), height-marginB, strings.Join(line, " "), xOf(dayNum(pts[len(pts)-1].Date)), height-marginB)

	fmt.Fprintf(&b, `<polygon points="%s" fill="%s" opacity="%s"/>`+"\n", area, th.Line, th.AreaOp)
	fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`+"\n", strings.Join(line, " "), th.Line)

	// End-point marker.
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3.5" fill="%s"/>`+"\n", xOf(dayNum(pts[len(pts)-1].Date)), yOf(current), th.Line)

	// Captions: truncation notice left, freshness right.
	if d.PrefixTruncated {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" fill="%s">history before %s unavailable</text>`+"\n", marginL, height-10, th.Muted, d.ObservedSince)
	}

	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" text-anchor="end" fill="%s">updated through %s</text>`+"\n", width-marginR, height-10, th.Muted, d.LastChecked)

	b.WriteString(`</svg>` + "\n")

	return b.String()
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

	return r.Replace(s)
}

func maxStars(pts []chartdata.Point) int {
	m := 1
	for _, p := range pts {
		if p.Stars > m {
			m = p.Stars
		}
	}

	return m
}

// niceCeil rounds up to a pleasant axis maximum (1/2/2.5/5 times a power of
// ten) and picks a segment count that keeps every gridline label round.
func niceCeil(v int) (int, int) {
	if v <= 4 {
		return 4, 4
	}

	mag := math.Pow(10, math.Floor(math.Log10(float64(v))))
	for _, m := range []struct {
		mult     float64
		segments int
	}{{1, 4}, {2, 4}, {2.5, 5}, {5, 5}, {10, 4}} {
		if c := m.mult * mag; float64(v) <= c {
			return int(c), m.segments
		}
	}

	return int(10 * mag), 4
}

func group(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}

		out = append(out, c)
	}

	return string(out)
}

func dayNum(date string) int {
	t, err := time.Parse(chartdata.DateFormat, date)
	if err != nil {
		return 0
	}

	return int(t.Unix() / 86400)
}

type tick struct {
	day   string
	label string
}

// xTicks picks 3 to 6 tick positions: year starts for multi-year spans,
// month starts otherwise.
func xTicks(first, last string) []tick {
	t0, err0 := time.Parse(chartdata.DateFormat, first)
	t1, err1 := time.Parse(chartdata.DateFormat, last)

	if err0 != nil || err1 != nil || !t1.After(t0) {
		return nil
	}

	spanDays := int(t1.Sub(t0).Hours() / 24)

	var ticks []tick

	if spanDays > 500 {
		step := (t1.Year()-t0.Year())/6 + 1
		for y := t0.Year() + 1; y <= t1.Year(); y += step {
			ticks = append(ticks, tick{day: fmt.Sprintf("%04d-01-01", y), label: fmt.Sprintf("%d", y)})
		}

		return ticks
	}

	months := (t1.Year()-t0.Year())*12 + int(t1.Month()) - int(t0.Month())
	step := months/5 + 1

	cur := time.Date(t0.Year(), t0.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	for i := 0; !cur.After(t1); i++ {
		if i%step == 0 {
			ticks = append(ticks, tick{day: cur.Format(chartdata.DateFormat), label: cur.Format("Jan 2006")})
		}

		cur = cur.AddDate(0, 1, 0)
	}

	return ticks
}
