package tui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/laixintao/termfana/internal/config"
	"github.com/laixintao/termfana/internal/metrics"
)

func modelFixture(t *testing.T) *Model {
	t.Helper()
	cfg := config.Default()
	cfg.Connection.URL = "http://127.0.0.1:8080/metrics"
	cfg.Window = "5m"
	cfg.Panels = []metrics.Selection{{Metric: "http_requests_total", View: "rate"}, {Metric: "http_duration_seconds", View: "p95"}, {Metric: "memory_bytes", View: "raw"}, {Metric: "workers", View: "raw"}}
	m := New(context.Background(), cfg, nil, true)
	for i := 1; i <= 60; i++ {
		x := float64(i)
		body := fmt.Sprintf("# HELP http_requests_total Completed HTTP requests\n# TYPE http_requests_total counter\nhttp_requests_total{method=\"GET\",status=\"200\"} %f\nhttp_requests_total{method=\"GET\",status=\"500\"} %f\n# TYPE memory_bytes gauge\nmemory_bytes %f\n# TYPE workers gauge\nworkers %f\n# TYPE http_duration_seconds histogram\nhttp_duration_seconds_bucket{le=\"0.1\"} %f\nhttp_duration_seconds_bucket{le=\"0.25\"} %f\nhttp_duration_seconds_bucket{le=\"1\"} %f\nhttp_duration_seconds_bucket{le=\"+Inf\"} %f\nhttp_duration_seconds_count %f\nhttp_duration_seconds_sum %f\n", 200*x+100*math.Sin(x/4), 10*x+15*math.Sin(x/3), (128+10*math.Sin(x/6))*1024*1024, 20+10*math.Sin(x/3), 40*x+20*math.Sin(x/3), 85*x+10*math.Sin(x/3), 99*x+5*math.Sin(x/3), 100*x, 100*x, 40*x)
		s, err := metrics.Parse([]byte(body), "text/plain", 10000)
		if err != nil {
			t.Fatal(err)
		}
		s.At = time.Unix(1700000000+int64(i*5), 0)
		s.Duration = 4 * time.Millisecond
		if i == 40 {
			s = metrics.Snapshot{At: s.At, Duration: s.Duration, Error: "connection interrupted"}
		}
		m.Update(scrapeMsg(s))
	}
	return m
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func TestTerminalLayouts(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 36}, {132, 42}, {180, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := modelFixture(t)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for _, mode := range []string{"dashboard", "browser", "labels", "series"} {
				m.mode = mode
				view := m.View()
				lines := strings.Split(strings.TrimSuffix(view.Content, "\n"), "\n")
				if len(lines) > size[1] {
					t.Errorf("%s height %d > %d", mode, len(lines), size[1])
				}
				for i, line := range lines {
					if w := ansi.StringWidth(line); w > size[0] {
						t.Errorf("%s row %d width %d > %d: %q", mode, i, w, size[0], ansi.Strip(line))
					}
				}
				if dir := os.Getenv("TERMFANA_RENDER_DIR"); dir != "" {
					os.MkdirAll(dir, 0755)
					name := fmt.Sprintf("%s-%dx%d", mode, size[0], size[1])
					os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(view.Content), 0600)
					os.WriteFile(filepath.Join(dir, name+".txt"), []byte(ansi.Strip(view.Content)), 0600)
				}
			}
		})
	}
}

func TestKeyboardWorkflow(t *testing.T) {
	m := modelFixture(t)
	m.Update(tea.WindowSizeMsg{Width: 132, Height: 42})
	m.Update(key('d'))
	if len(m.cfg.Panels) != 3 {
		t.Fatal("remove panel")
	}
	m.Update(key('a'))
	if m.mode != "browser" {
		t.Fatal("open browser")
	}
	m.filter = "http_requests"
	m.Update(key(tea.KeyEnter))
	if len(m.cfg.Panels) != 4 || m.mode != "dashboard" {
		t.Fatal("add metric")
	}
	m.Update(key('l'))
	if m.mode != "labels" {
		t.Fatal("label picker")
	}
	m.Update(key(tea.KeyEnter))
	if len(m.cfg.Panels[m.focused].Labels) != 1 {
		t.Fatal("label selection")
	}
	m.Update(key(tea.KeyEscape))
	m.Update(key('g'))
	if m.mode != "series" {
		t.Fatal("series picker")
	}
	m.Update(key(tea.KeyEnter))
	if m.mode != "detail" || !strings.Contains(m.detail, "Labels:") {
		t.Fatal("full label details")
	}
	m.Update(key(tea.KeyEscape))
	m.Update(key(tea.KeySpace))
	if len(m.cfg.Panels[m.focused].Hidden) != 1 {
		t.Fatal("hide series")
	}
	m.Update(key(tea.KeyEscape))
	m.Update(key(tea.KeyLeft))
	if m.cursor.IsZero() || m.end.IsZero() {
		t.Fatal("cursor must freeze shared time")
	}
	w := m.window
	m.Update(key('+'))
	if m.window != w/2 {
		t.Fatal("zoom")
	}
	m.Update(key('r'))
	if !m.end.IsZero() || !m.cursor.IsZero() {
		t.Fatal("return to live")
	}
	m.Update(key(tea.KeyEnter))
	if !m.fullscreen {
		t.Fatal("maximize")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View().Content, "80×24") {
		t.Fatal("small terminal hint")
	}
}

func TestTraceGapsAndSeriesLimit(t *testing.T) {
	h := metrics.NewHistory(10, 100)
	for n := range 3 {
		var b strings.Builder
		b.WriteString("# TYPE gauge gauge\n")
		for i := range 12 {
			fmt.Fprintf(&b, "gauge{id=\"%02d\"} %d\n", i, i+n)
		}
		s, _ := metrics.Parse([]byte(b.String()), "text/plain", 100)
		s.At = time.Unix(int64(n+1), 0)
		if n == 1 {
			s = metrics.Snapshot{At: s.At, Error: "offline"}
		}
		h.Append(s)
	}
	sel := metrics.Selection{Metric: "gauge", View: "raw"}
	data := buildPanel(h.Snapshots(), sel)
	if data.total != 12 || len(data.series) != 8 {
		t.Fatalf("series bound: %d/%d", len(data.series), data.total)
	}
	if data.series[0].Points[1].Value != nil || data.series[0].Points[2].Value == nil {
		t.Fatal("failed scrape must create a gap and recover")
	}
	first := data.series[0].Key
	sel.Hidden = []string{first}
	next := buildPanel(h.Snapshots(), sel)
	if next.series[0].Key == first || len(next.series) != 8 {
		t.Fatal("hidden series must make room for next series")
	}
}

func TestSessionSaveTakesOwnedSnapshot(t *testing.T) {
	m := modelFixture(t)
	m.cfg.Panels[0].Labels = metrics.Labels{"method": "GET"}
	path := filepath.Join(t.TempDir(), "session.json")
	m.startInput("save", path)
	_, cmd := m.edit(key(tea.KeyEnter))
	m.cfg.Panels[0].Labels["method"] = "POST"
	msg := cmd().(savedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Panels[0].Labels["method"] != "GET" {
		t.Fatal("async save must not share mutable state")
	}
}

func TestPastedInput(t *testing.T) {
	m := modelFixture(t)
	m.startInput("search", "")
	m.Update(tea.PasteMsg{Content: "http_requests"})
	if m.filter != "http_requests" {
		t.Fatal("terminal paste did not reach the text input")
	}
}

func searchFixture(t *testing.T) *Model {
	t.Helper()
	m := modelFixture(t)
	m.cfg.Panels = nil
	m.mode = "browser"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(key('/'))
	for _, r := range "http" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestSearchNavigationAndSelection(t *testing.T) {
	m := searchFixture(t)
	if len(m.items()) != 2 {
		t.Fatalf("expected two matching metrics, got %v", m.items())
	}
	for _, step := range []struct {
		code rune
		row  int
	}{
		{tea.KeyUp, 0}, {tea.KeyDown, 1}, {tea.KeyDown, 1},
		{tea.KeyUp, 0}, {tea.KeyPgDown, 1}, {tea.KeyPgUp, 0}, {tea.KeyDown, 1},
	} {
		m.Update(key(step.code))
		if m.row != step.row || m.filter != "http" || m.inputMode != "search" {
			t.Fatalf("%s: row=%d, filter=%q, inputMode=%q", key(step.code).String(), m.row, m.filter, m.inputMode)
		}
	}
	// Cursor movement, key releases, blinking, and background scrapes must not
	// move the highlighted result back to the first row.
	for _, msg := range []tea.Msg{
		key(tea.KeyLeft), key(tea.KeyRight), key(tea.KeyHome), key(tea.KeyEnd),
		tea.KeyReleaseMsg{Code: tea.KeyDown}, cursor.BlinkMsg{},
		scrapeMsg(m.lastSuccess), tea.WindowSizeMsg{Width: 132, Height: 42},
	} {
		m.Update(msg)
		m.View()
		if m.row != 1 || m.filter != "http" {
			t.Fatalf("%T reset search selection: row=%d, filter=%q", msg, m.row, m.filter)
		}
	}
	m.Update(key(tea.KeyEnter))
	if m.mode != "dashboard" || m.inputMode != "" || len(m.cfg.Panels) != 1 {
		t.Fatalf("Enter did not add the selected metric: mode=%s, inputMode=%s, panels=%v", m.mode, m.inputMode, m.cfg.Panels)
	}
	if panel := m.cfg.Panels[0]; panel.Metric != "http_requests_total" || panel.View != "rate" {
		t.Fatalf("Enter added the wrong metric: %+v", panel)
	}
}

func TestSearchEditingAndEscape(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		msg        tea.Msg
	}{
		{"typing", "http_", tea.KeyPressMsg{Code: '_', Text: "_"}},
		{"paste", "http_", tea.PasteMsg{Content: "_"}},
		{"delete", "htt", key(tea.KeyBackspace)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := searchFixture(t)
			m.Update(key(tea.KeyDown))
			m.Update(tc.msg)
			if m.filter != tc.want || m.row != 0 || m.inputMode != "search" {
				t.Fatalf("editing must update the filter and reset selection: filter=%q row=%d inputMode=%q", m.filter, m.row, m.inputMode)
			}
		})
	}
	m := searchFixture(t)
	m.Update(key(tea.KeyDown))
	m.Update(key(tea.KeyEscape))
	if m.inputMode != "" || m.filter != "http" || m.row != 1 || len(m.cfg.Panels) != 0 {
		t.Fatal("Escape should leave search editing without selecting or resetting the result")
	}
	m.Update(key(tea.KeyEnter))
	if m.cfg.Panels[0].Metric != "http_requests_total" {
		t.Fatal("selection was lost after leaving search")
	}
}

func TestSearchWithNoMatches(t *testing.T) {
	m := searchFixture(t)
	// Shortcut letters and spaces are search text while the input is focused.
	for _, r := range "jkq /" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	for _, code := range []rune{tea.KeyDown, tea.KeyUp, tea.KeyPgDown, tea.KeyPgUp, tea.KeyEnter} {
		m.Update(key(code))
	}
	if m.filter != "httpjkq /" || m.row != 0 || m.inputMode != "search" || len(m.cfg.Panels) != 0 {
		t.Fatal("empty search results must remain editable without selecting a metric")
	}
}

func TestSearchInLabelAndSeriesPickers(t *testing.T) {
	for _, mode := range []string{"labels", "series"} {
		t.Run(mode, func(t *testing.T) {
			m := modelFixture(t)
			m.mode = mode
			m.Update(key('/'))
			m.Update(tea.PasteMsg{Content: "status"})
			m.Update(key(tea.KeyDown))
			m.Update(key(tea.KeyEnter))
			if m.inputMode != "" {
				t.Fatal("Enter should finish editing the search")
			}
			if mode == "labels" && m.cfg.Panels[0].Labels["status"] != "500" {
				t.Fatal("Enter should toggle the highlighted label")
			}
			if mode == "series" && (m.mode != "detail" || !strings.Contains(m.detail, "500")) {
				t.Fatal("Enter should inspect the highlighted series")
			}
		})
	}
}
