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

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"termfana/internal/config"
	"termfana/internal/metrics"
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
