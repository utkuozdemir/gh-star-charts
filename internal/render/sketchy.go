package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/utkuozdemir/gh-star-charts/internal/chartdata"
)

// sketchSVG mimics the classic star-history embed look, which is the default:
// centered "Star History" title, a legend chip, hand-drawn axes titled "Date"
// and "GitHub Stars", k-formatted counts, a wobbly coral line with dot
// markers, comic lettering.
//
// The hand-drawn wobble is deterministic: its PRNG is seeded by the repo name
// and theme, so re-rendering unchanged data is byte-identical.
func sketchSVG(d *chartdata.Data, th Theme) string {
	const (
		w     = 800
		h     = 533
		left  = 92
		right = 40
		top   = 96
		bot   = 76
	)

	pts := d.Points

	if n := len(pts); n > 0 && d.LastChecked > pts[n-1].Date {
		pts = append(append([]chartdata.Point{}, pts...), chartdata.Point{Date: d.LastChecked, Stars: pts[n-1].Stars})
	}

	var b strings.Builder

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="Star history of %s">`+"\n",
		w, h, w, h, esc(d.Repo))
	b.WriteString(`<style>text{font-family:"Comic Sans MS","Chalkboard SE","Comic Neue","Marker Felt",cursive}</style>` + "\n")

	if th.Background != "" {
		fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" rx="6" fill="%s"/>`+"\n", w, h, th.Background)
	}

	fmt.Fprintf(&b, `<text x="%d" y="52" font-size="26" font-weight="bold" text-anchor="middle" fill="%s">Star History</text>`+"\n", w/2, th.Primary)

	if len(pts) == 0 {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="15" text-anchor="middle" fill="%s">no data yet</text>`+"\n", w/2, h/2, th.Muted)
		b.WriteString(`</svg>` + "\n")

		return b.String()
	}

	rnd := newRNG(d.Repo + "/" + th.Name)

	// Legend chip: wobbly rounded border, square marker, repo name. The name
	// is capped at a fixed character budget and its glyphs are forced into
	// the reserved width, so no viewer font choice can overflow the border.
	name := d.Repo
	if len(name) > 46 {
		name = name[:45] + "\u2026"
	}

	textW := 9 * len([]rune(name))
	legendW := 30 + textW

	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="34" rx="8" fill="none" stroke="%s" stroke-width="2" transform="rotate(%.2f %d %d)"/>`+"\n",
		left+8, top-26, legendW, th.Primary, 0.3*rnd.next(), left+8+legendW/2, top-26+17)
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="11" height="11" rx="2" fill="%s"/>`+"\n", left+18, top-14, th.Line)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="15" fill="%s" textLength="%d" lengthAdjust="spacingAndGlyphs">%s</text>`+"\n", left+36, top-3, th.Primary, textW-9, esc(name))

	x0, x1 := dayNum(pts[0].Date), dayNum(pts[len(pts)-1].Date)
	if x1 == x0 {
		x1 = x0 + 1
	}

	yMax, ySegments := niceCeil(maxStars(pts))

	xOf := func(day int) float64 {
		return left + float64(day-x0)/float64(x1-x0)*(w-left-right)
	}
	yOf := func(stars int) float64 {
		return float64(h-bot) - float64(stars)/float64(yMax)*(h-top-bot)
	}

	// Ticks and labels. No grid, like the original.
	for s := 1; s <= ySegments; s++ {
		v := yMax / ySegments * s
		y := yOf(v)

		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="2"/>`+"\n", left-7, y, left, y, th.Primary)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="15" text-anchor="end" fill="%s">%s</text>`+"\n", left-13, y+5, th.Primary, kFormat(v))
	}

	for _, tick := range xTicks(pts[0].Date, pts[len(pts)-1].Date) {
		x := xOf(dayNum(tick.day))

		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="2"/>`+"\n", x, h-bot, x, h-bot+7, th.Primary)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="15" text-anchor="middle" fill="%s">%s</text>`+"\n", x, h-bot+27, th.Primary, tick.label)
	}

	// Axis titles.
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="17" text-anchor="middle" fill="%s">Date</text>`+"\n", (left+w-right)/2, h-18, th.Primary)
	fmt.Fprintf(&b, `<text x="26" y="%d" font-size="17" text-anchor="middle" fill="%s" transform="rotate(-90 26 %d)">GitHub Stars</text>`+"\n",
		(top+h-bot)/2, th.Primary, (top+h-bot)/2)

	// Hand-drawn axes.
	axes := wobblePath(
		[]float64{left, left, w - right},
		[]float64{top - 6, h - bot, h - bot},
		1.2, rnd,
	)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>`+"\n", axes, th.Primary)

	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))

	for i, p := range pts {
		xs[i] = xOf(dayNum(p.Date))
		ys[i] = yOf(p.Stars)
	}

	lineXs, lineYs := downsample(xs, ys, polylineBudget)

	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>`+"\n",
		wobblePath(lineXs, lineYs, 1.5, rnd), th.Line)

	for _, i := range markerIndices(len(pts), 26) {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s"/>`+"\n", xs[i], ys[i], th.Line)
	}

	if d.PrefixTruncated {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" fill="%s">history before %s unavailable</text>`+"\n", left, h-4, th.Muted, d.ObservedSince)
	}

	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" text-anchor="end" fill="%s">updated through %s</text>`+"\n", w-10, h-4, th.Muted, d.LastChecked)

	b.WriteString(`</svg>` + "\n")

	return b.String()
}

// kFormat renders counts the way the original chart did: 500, 1.0k, 2.5k.
func kFormat(v int) string {
	switch {
	case v < 1000:
		return fmt.Sprintf("%d", v)
	case v < 1000000:
		return fmt.Sprintf("%.1fk", float64(v)/1000)
	default:
		return fmt.Sprintf("%.1fm", float64(v)/1000000)
	}
}

type rng struct{ state uint64 }

func newRNG(seed string) *rng {
	// FNV-1a.
	var h uint64 = 14695981039346656037
	for _, c := range []byte(seed) {
		h ^= uint64(c)
		h *= 1099511628211
	}

	if h == 0 {
		h = 1
	}

	return &rng{state: h}
}

// next returns a deterministic value in [-1, 1).
func (r *rng) next() float64 {
	// splitmix64.
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31

	return float64(z>>11)/float64(1<<52) - 1
}

// wobblePath renders a polyline as a hand-drawn path: segments are subdivided
// and interior points get a small perpendicular offset.
func wobblePath(xs, ys []float64, amp float64, r *rng) string {
	if len(xs) == 0 {
		return ""
	}

	var b strings.Builder

	fmt.Fprintf(&b, "M %.1f,%.1f", xs[0], ys[0])

	for i := 1; i < len(xs); i++ {
		x0, y0, x1, y1 := xs[i-1], ys[i-1], xs[i], ys[i]
		dx, dy := x1-x0, y1-y0

		length := math.Hypot(dx, dy)
		if length == 0 {
			continue
		}

		nx, ny := -dy/length, dx/length

		steps := int(length/22) + 1
		for s := 1; s <= steps; s++ {
			t := float64(s) / float64(steps)
			// The explicit conversions force intermediate rounding, which
			// keeps amd64 and arm64 from fusing multiply-adds differently
			// and producing byte-different output for identical data.
			px, py := float64(x0+float64(dx*t)), float64(y0+float64(dy*t))

			if s < steps {
				o := amp * r.next()
				px = float64(px + float64(nx*o))
				py = float64(py + float64(ny*o))
			}

			fmt.Fprintf(&b, " L %.1f,%.1f", px, py)
		}
	}

	return b.String()
}

// polylineBudget bounds the number of points the drawn line uses, so a chart
// file stays constant-size no matter how many years of daily points the data
// file accumulates.
const polylineBudget = 320

// downsample reduces parallel coordinate slices to the budget, keeping the
// first point, the last point, and the extremum of each bucket so a one-day
// dip cannot vanish from the drawn line.
func downsample(xs, ys []float64, budget int) ([]float64, []float64) {
	n := len(xs)
	if n <= budget {
		return xs, ys
	}

	outX := []float64{xs[0]}
	outY := []float64{ys[0]}

	buckets := budget - 2
	for bkt := 0; bkt < buckets; bkt++ {
		lo := 1 + bkt*(n-2)/buckets
		hi := 1 + (bkt+1)*(n-2)/buckets

		if lo >= hi {
			continue
		}

		// Keep the point that deviates most from the straight line between
		// the bucket's neighbors, so a dip or spike survives sampling.
		x0, y0 := outX[len(outX)-1], outY[len(outY)-1]
		x1, y1 := xs[hi-1], ys[hi-1]

		pick, best := lo, -1.0

		for i := lo; i < hi; i++ {
			var expected float64
			if x1 == x0 {
				expected = y0
			} else {
				expected = y0 + (y1-y0)*(xs[i]-x0)/(x1-x0)
			}

			if dev := math.Abs(ys[i] - expected); dev > best {
				pick, best = i, dev
			}
		}

		outX = append(outX, xs[pick])
		outY = append(outY, ys[pick])
	}

	outX = append(outX, xs[n-1])
	outY = append(outY, ys[n-1])

	return outX, outY
}

// markerIndices picks up to budget evenly spaced point indices, always
// including the first and last.
func markerIndices(n, budget int) []int {
	if n <= budget {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}

		return out
	}

	out := make([]int, 0, budget)
	for s := 0; s < budget; s++ {
		out = append(out, s*(n-1)/(budget-1))
	}

	return out
}
