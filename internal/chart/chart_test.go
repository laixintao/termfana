package chart

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func val(v float64) *float64 { return &v }

func TestGapsAreNotConnected(t *testing.T) {
	start := time.Unix(1000, 0)
	s := []Series{{Points: []Point{{start, val(0)}, {start.Add(5 * time.Second), nil}, {start.Add(10 * time.Second), val(10)}}}}
	got := ansi.Strip(Render(s, Options{Width: 49, Height: 10, Start: start, End: start.Add(10 * time.Second), ASCII: true}))
	for _, line := range strings.Split(got, "\n")[:9] {
		if line[9+20] != ' ' {
			t.Fatalf("gap connected: %s", got)
		}
	}
}

func TestDenseSpikeSurvivesRendering(t *testing.T) {
	start := time.Unix(1000, 0)
	s := Series{}
	for i := range 1001 {
		value := 0.0
		if i == 501 {
			value = 100
		}
		s.Points = append(s.Points, Point{start.Add(time.Duration(i) * time.Second), val(value)})
	}
	got := ansi.Strip(Render([]Series{s}, Options{Width: 49, Height: 10, Start: start, End: start.Add(1000 * time.Second), ASCII: true}))
	if !strings.Contains(strings.Split(got, "\n")[0][9:], "1") {
		t.Fatalf("spike lost: %s", got)
	}
}

func TestChartBoundsAndDegenerateValues(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, values := range [][]*float64{{val(-2), val(-2)}, {nil, nil}, {val(math.NaN()), val(math.Inf(1))}, {val(-3), val(5)}, {val(-math.MaxFloat64), val(math.MaxFloat64)}, {val(math.MaxFloat64), val(math.MaxFloat64)}} {
		series := []Series{{Points: []Point{{start, values[0]}, {start.Add(time.Second), values[1]}}}}
		for _, ascii := range []bool{true, false} {
			got := Render(series, Options{Width: 40, Height: 8, Start: start, End: start.Add(time.Second), ASCII: ascii})
			lines := strings.Split(got, "\n")
			if len(lines) != 8 {
				t.Fatal("wrong height")
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > 40 {
					t.Fatalf("line too wide: %q", line)
				}
			}
		}
	}
}

func TestAxisDistinguishesSmallChangesInLargeValues(t *testing.T) {
	labels := axisLabels(100000000, 100055000)
	if labels[0] == labels[1] || labels[1] == labels[2] {
		t.Fatalf("axis rounded away the trend: %v", labels)
	}
}
