// Package chart renders bounded time series without interpolating across gaps.
package chart

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

var Palette = []string{"#58D5C9", "#F4C874", "#A6A0F5", "#F3899A", "#81B5F5", "#B8DA81", "#E9A975", "#83CDD9"}

type Point struct {
	At    time.Time
	Value *float64
}
type Series struct {
	Key, Label string
	Points     []Point
}
type Options struct {
	Width, Height int
	Start, End    time.Time
	Cursor        time.Time
	ASCII         bool
}

func Number(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e12:
		return fmt.Sprintf("%.3gT", v/1e12)
	case a >= 1e9:
		return fmt.Sprintf("%.3gG", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.3gM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("%.3gk", v/1e3)
	case a != 0 && a < .001:
		return fmt.Sprintf("%.1e", v)
	default:
		return fmt.Sprintf("%.3g", v)
	}
}

func axisLabels(low, high float64) [3]string {
	peak := math.Max(math.Abs(low), math.Abs(high))
	scale, suffix := 1.0, ""
	for _, unit := range []struct {
		value  float64
		suffix string
	}{{1e12, "T"}, {1e9, "G"}, {1e6, "M"}, {1e3, "k"}} {
		if peak >= unit.value {
			scale, suffix = unit.value, unit.suffix
			break
		}
	}
	span := high/scale - low/scale
	precision := 0
	if span > 0 {
		precision = max(0, min(8, 2-int(math.Floor(math.Log10(span)))))
	}
	values := [3]float64{high, low/2 + high/2, low}
	var labels [3]string
	for i, value := range values {
		if peak/scale >= 1e6 || (peak > 0 && peak/scale < 1e-6) {
			labels[i] = fmt.Sprintf("%.4g", value)
		} else {
			labels[i] = fmt.Sprintf("%.*f%s", precision, value/scale, suffix)
		}
	}
	return labels
}

func Render(series []Series, o Options) string {
	if o.Width < 16 || o.Height < 4 {
		return "Chart needs more space"
	}
	h := o.Height - 1
	minV, maxV := math.Inf(1), math.Inf(-1)
	for _, s := range series {
		for _, p := range s.Points {
			if p.Value == nil || p.At.Before(o.Start) || p.At.After(o.End) {
				continue
			}
			v := *p.Value
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			minV = math.Min(minV, v)
			maxV = math.Max(maxV, v)
		}
	}
	hasData := !math.IsInf(minV, 1)
	if !hasData {
		minV, maxV = 0, 1
	}
	if minV == maxV {
		pad := math.Max(math.Abs(minV)*.05, .01)
		minV = math.Max(-math.MaxFloat64, minV-pad)
		maxV = math.Min(math.MaxFloat64, maxV+pad)
	}
	axisText := axisLabels(minV, maxV)
	axisWidth := 7
	for _, label := range axisText {
		axisWidth = max(axisWidth, len(label))
	}
	w := o.Width - axisWidth - 2
	if w < 2 {
		return "Chart needs more space"
	}
	normalizer := math.Max(1, math.Max(math.Abs(minV), math.Abs(maxV)))
	lowScaled, highScaled := minV/normalizer, maxV/normalizer
	sx, sy := 2, 4
	if o.ASCII {
		sx, sy = 1, 1
	}
	pw, ph := w*sx, h*sy
	bits := make([]uint8, w*h)
	owners := make([]int, w*h)
	for i := range owners {
		owners[i] = -1
	}
	dot := [4][2]uint8{{1, 8}, {2, 16}, {4, 32}, {64, 128}}
	plot := func(x, y, owner int) {
		x = max(0, min(pw-1, x))
		y = max(0, min(ph-1, y))
		idx := (y/sy)*w + x/sx
		if o.ASCII {
			bits[idx] = 1
		} else {
			bits[idx] |= dot[y%sy][x%sx]
		}
		owners[idx] = owner
	}
	line := func(x0, y0, x1, y1, owner int) {
		dx, dy := x1-x0, y1-y0
		steps := max(abs(dx), abs(dy))
		if steps == 0 {
			plot(x0, y0, owner)
			return
		}
		for n := 0; n <= steps; n++ {
			plot(x0+int(math.Round(float64(dx*n)/float64(steps))), y0+int(math.Round(float64(dy*n)/float64(steps))), owner)
		}
	}
	span := o.End.Sub(o.Start).Seconds()
	if span <= 0 {
		span = 1
	}
	for n, s := range series {
		px, py, connected := 0, 0, false
		for _, p := range s.Points {
			if p.Value == nil || p.At.Before(o.Start) || p.At.After(o.End) || math.IsNaN(*p.Value) || math.IsInf(*p.Value, 0) {
				connected = false
				continue
			}
			x := int(p.At.Sub(o.Start).Seconds() / span * float64(pw-1))
			y := 0
			if highScaled > lowScaled {
				y = int((highScaled - *p.Value/normalizer) / (highScaled - lowScaled) * float64(ph-1))
			}
			if connected {
				line(px, py, x, y, n)
			} else {
				plot(x, y, n)
			}
			px, py, connected = x, y, true
		}
	}
	cursor := -1
	if !o.Cursor.IsZero() && !o.Cursor.Before(o.Start) && !o.Cursor.After(o.End) {
		cursor = min(w-1, int(o.Cursor.Sub(o.Start).Seconds()/span*float64(w-1)))
	}
	rows := make([]string, h+1)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#65748B"))
	colors := make([]lipgloss.Style, len(Palette))
	for i, c := range Palette {
		colors[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	for y := 0; y < h; y++ {
		axis := strings.Repeat(" ", axisWidth)
		if y == 0 {
			axis = fmt.Sprintf("%*s", axisWidth, axisText[0])
		} else if y == h-1 {
			axis = fmt.Sprintf("%*s", axisWidth, axisText[2])
		} else if y == h/2 {
			axis = fmt.Sprintf("%*s", axisWidth, axisText[1])
		}
		var b strings.Builder
		b.WriteString(muted.Render(axis + " |"))
		for x := 0; x < w; x++ {
			i := y*w + x
			if bits[i] == 0 {
				if x == cursor {
					glyph := "┊"
					if o.ASCII {
						glyph = "|"
					}
					b.WriteString(muted.Render(glyph))
				} else {
					b.WriteByte(' ')
				}
				continue
			}
			glyph := string(rune(0x2800) + rune(bits[i]))
			if o.ASCII {
				glyph = string(rune('1' + owners[i]%8))
			}
			b.WriteString(colors[owners[i]%len(colors)].Render(glyph))
		}
		rows[y] = b.String()
	}
	left, right := o.Start.Format("15:04:05"), o.End.Format("15:04:05")
	prefix := strings.Repeat(" ", axisWidth+2)
	if w < len(left)+len(right) {
		left = ""
	}
	if w < len(right) {
		right = ""
	}
	rows[h] = muted.Render(prefix + left + strings.Repeat(" ", max(0, w-len(left)-len(right))) + right)
	if !hasData && h > 2 {
		message := "Collecting samples / no data"
		if len(message) > w {
			message = "No data"
		}
		if len(message) > w {
			message = ""
		}
		rows[h/2] = muted.Render(prefix + message + strings.Repeat(" ", max(0, w-len(message))))
	}
	return strings.Join(rows, "\n")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
