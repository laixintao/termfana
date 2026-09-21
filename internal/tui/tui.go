package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/laixintao/termfana/internal/chart"
	"github.com/laixintao/termfana/internal/config"
	"github.com/laixintao/termfana/internal/metrics"
)

var (
	accent        = lipgloss.NewStyle().Foreground(lipgloss.Color("#58D5C9"))
	muted         = lipgloss.NewStyle().Foreground(lipgloss.Color("#8793A7"))
	bright        = lipgloss.NewStyle().Foreground(lipgloss.Color("#E4EAF3"))
	warning       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F4C874"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#101D25")).Background(lipgloss.Color("#58D5C9"))
)

type scrapeMsg metrics.Snapshot
type tickMsg struct{}
type savedMsg struct {
	path string
	err  error
}

type Model struct {
	cfg                              config.Config
	scraper                          *metrics.Scraper
	ctx                              context.Context
	history                          *metrics.History
	latest, lastSuccess              metrics.Snapshot
	width, height                    int
	mode, filter, inputMode, message string
	detail, detailReturn             string
	detailOffset                     int
	input                            textinput.Model
	row, focused                     int
	fullscreen, demo, help           bool
	window                           time.Duration
	end, cursor                      time.Time
	cache                            map[int]panelData
}

func New(ctx context.Context, cfg config.Config, scraper *metrics.Scraper, demo bool) *Model {
	window, _ := time.ParseDuration(cfg.Window)
	input := textinput.New()
	input.CharLimit = 4096
	input.Prompt = "> "
	mode := "browser"
	if len(cfg.Panels) > 0 {
		mode = "dashboard"
	}
	return &Model{cfg: cfg, scraper: scraper, ctx: ctx, history: metrics.NewHistory(cfg.Capacity, cfg.MaxSeries), mode: mode, input: input, demo: demo, window: window, cache: map[int]panelData{}}
}

func Run(ctx context.Context, cfg config.Config, scraper *metrics.Scraper, demo bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := tea.NewProgram(New(ctx, cfg, scraper, demo), tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (m *Model) scrape() tea.Cmd { return func() tea.Msg { return scrapeMsg(m.scraper.Scrape(m.ctx)) } }
func (m *Model) Init() tea.Cmd   { return m.scrape() }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.sizeInput()
	case scrapeMsg:
		m.latest = metrics.Snapshot(msg)
		m.history.Append(m.latest)
		if m.latest.Error == "" {
			m.lastSuccess = m.latest
			for i := range m.cfg.Panels {
				p := &m.cfg.Panels[i]
				if p.View == "" || p.View == "auto" {
					if f, ok := metrics.ResolveFamily(m.latest, p.Metric); ok {
						p.View = metrics.DefaultView(f)
					}
				}
			}
		}
		m.cache = map[int]panelData{}
		frames := m.history.Snapshots()
		if !m.end.IsZero() && len(frames) > 0 && m.end.Before(frames[0].At) {
			m.end = frames[0].At
			m.cursor = m.end
			m.message = "Older history expired; moved to earliest retained sample"
		}
		if !m.cursor.IsZero() && len(frames) > 0 && m.cursor.Before(frames[0].At) {
			m.cursor = frames[0].At
			m.message = "Inspected sample expired; moved to earliest retained sample"
		}
		if m.history.Trimmed {
			m.message = "History shortened to keep the series catalog within its capacity"
		}
		interval, _ := time.ParseDuration(m.cfg.Interval)
		return m, tea.Tick(max(time.Millisecond, interval-m.latest.Duration), func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.scrape()
	case savedMsg:
		if msg.err != nil {
			m.message = "Save failed: " + msg.err.Error()
		} else {
			m.message = "Session saved to " + msg.path
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.inputMode != "" {
			return m.edit(msg)
		}
		if m.help {
			if key == "?" || key == "esc" || key == "q" {
				m.help = false
			}
			return m, nil
		}
		if key == "?" {
			m.help = true
			return m, nil
		}
		if key == "q" {
			return m, tea.Quit
		}
		if key == "esc" {
			if m.mode == "detail" {
				m.mode = m.detailReturn
				return m, nil
			}
			if len(m.cfg.Panels) > 0 {
				m.mode = "dashboard"
			}
			m.filter = ""
			m.row = 0
			return m, nil
		}
		if key == "s" && m.mode == "dashboard" {
			return m, m.startInput("save", "termfana-session.json")
		}
		if m.mode == "detail" {
			switch key {
			case "up", "k":
				m.detailOffset = max(0, m.detailOffset-1)
			case "down", "j":
				m.detailOffset++
			case "pgdown":
				m.detailOffset += max(1, m.height-12)
			case "pgup":
				m.detailOffset = max(0, m.detailOffset-m.height+12)
			}
			return m, nil
		}
		if m.mode != "dashboard" {
			return m.menuKey(key)
		}
		switch key {
		case "a", "/":
			m.mode = "browser"
			m.row = 0
			m.filter = ""
			if key == "/" {
				return m, m.startInput("search", "")
			}
		case "tab":
			if len(m.cfg.Panels) > 0 {
				m.focused = (m.focused + 1) % len(m.cfg.Panels)
			}
		case "shift+tab":
			if len(m.cfg.Panels) > 0 {
				m.focused = (m.focused + len(m.cfg.Panels) - 1) % len(m.cfg.Panels)
			}
		case "1", "2", "3", "4":
			i := int(key[0] - '1')
			if i < len(m.cfg.Panels) {
				m.focused = i
			}
		case "enter":
			m.fullscreen = !m.fullscreen
		case "d":
			if len(m.cfg.Panels) > 0 {
				m.cfg.Panels = append(m.cfg.Panels[:m.focused], m.cfg.Panels[m.focused+1:]...)
				m.focused = max(0, min(m.focused, len(m.cfg.Panels)-1))
				m.cache = map[int]panelData{}
			}
			if len(m.cfg.Panels) == 0 {
				m.mode = "browser"
			}
		case "v":
			if len(m.cfg.Panels) > 0 {
				p := &m.cfg.Panels[m.focused]
				f, _ := metrics.ResolveFamily(m.lastSuccess, p.Metric)
				views := metrics.Views(f)
				for i, v := range views {
					if p.View == v {
						p.View = views[(i+1)%len(views)]
						break
					}
				}
				delete(m.cache, m.focused)
			}
		case "l":
			m.mode = "labels"
			m.row = 0
			m.filter = ""
		case "g":
			m.mode = "series"
			m.row = 0
			m.filter = ""
		case "space":
			if m.end.IsZero() {
				m.end = m.latest.At
				m.cursor = m.end
			} else {
				m.end = time.Time{}
				m.cursor = time.Time{}
			}
		case "r", "home":
			m.end = time.Time{}
			m.cursor = time.Time{}
		case "+", "=":
			m.window = max(time.Second, m.window/2)
		case "-":
			m.window = min(7*24*time.Hour, m.window*2)
		case "[":
			m.pan(-1)
		case "]":
			m.pan(1)
		case "left", "h":
			m.moveCursor(-1)
		case "right":
			m.moveCursor(1)
		}
	default:
		if m.inputMode != "" {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			if m.inputMode == "search" {
				m.filter = m.input.Value()
				m.row = 0
			}
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) startInput(kind, value string) tea.Cmd {
	m.inputMode = kind
	m.sizeInput()
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *Model) sizeInput() {
	width := m.width - 14
	if m.inputMode == "save" {
		width = m.width - 38
	}
	m.input.SetWidth(max(8, width))
}

func (m *Model) edit(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.inputMode = ""
		m.input.Blur()
		return m, nil
	case "enter":
		kind, value := m.inputMode, m.input.Value()
		m.inputMode = ""
		m.input.Blur()
		if kind == "search" {
			m.filter = value
			m.row = 0
			return m, nil
		}
		if value == "" {
			m.message = "Enter a session file path"
			return m, nil
		}
		cfg := m.cfg.Clone()
		cfg.Window = m.window.String()
		return m, func() tea.Msg { return savedMsg{path: value, err: config.Save(value, cfg)} }
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		if m.inputMode == "search" {
			m.filter = m.input.Value()
			m.row = 0
		}
		return m, cmd
	}
}

type menuItem struct {
	title, detail, key, value string
	family                    metrics.Family
}

func (m *Model) items() []menuItem {
	items := []menuItem{}
	switch m.mode {
	case "browser":
		for _, f := range m.lastSuccess.Families {
			if f.Series > 0 {
				items = append(items, menuItem{title: f.Name, detail: fmt.Sprintf("%-10s %5d series  %s", f.Type, f.Series, f.Help), family: f})
			}
		}
	case "labels":
		if len(m.cfg.Panels) == 0 {
			break
		}
		p := m.cfg.Panels[m.focused]
		seen := map[string]bool{}
		for key := range m.lastSuccess.Values {
			d := m.lastSuccess.Definitions[key]
			if d.Family != p.Metric && d.Name != p.Metric {
				continue
			}
			for k, v := range d.Labels {
				if k == "le" && p.View != "raw" {
					continue
				}
				id := (metrics.Labels{k: v}).String()
				if seen[id] {
					continue
				}
				seen[id] = true
				mark := "[ ]"
				if value, ok := p.Labels[k]; ok && value == v {
					mark = "[x]"
				}
				items = append(items, menuItem{title: id, detail: mark, key: k, value: v})
			}
		}
	case "series":
		if len(m.cfg.Panels) == 0 {
			break
		}
		data := m.data(m.focused)
		p := m.cfg.Panels[m.focused]
		hidden := map[string]bool{}
		for _, key := range p.Hidden {
			hidden[key] = true
		}
		for _, r := range data.available {
			mark := "[ ] eligible"
			if hidden[r.Key()] {
				mark = "[-] hidden"
			}
			for _, s := range data.series {
				if s.Key == r.Key() {
					mark = "[x] plotted"
					break
				}
			}
			items = append(items, menuItem{title: r.Key(), detail: mark, key: r.Key()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].title < items[j].title })
	if m.filter != "" {
		filtered := items[:0]
		needle := strings.ToLower(m.filter)
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.title+" "+item.detail), needle) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items
}

func (m *Model) menuKey(key string) (tea.Model, tea.Cmd) {
	items := m.items()
	if (key == "i" || (key == "enter" && m.mode == "series")) && len(items) > 0 {
		item := items[min(m.row, len(items)-1)]
		m.detail = Safe(item.title) + "\n\n" + Safe(item.detail)
		if m.mode == "browser" {
			m.detail += "\n\n" + Safe(item.family.Help)
		}
		if m.mode == "series" {
			data := m.data(m.focused)
			for _, r := range data.available {
				if r.Key() == item.key {
					m.detail = "Metric: " + Safe(r.Metric) + "\nView: " + m.cfg.Panels[m.focused].View + "\n\nLabels:\n"
					keys := []string{}
					for k := range r.Labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						m.detail += "  " + Safe(k) + " = " + Safe(r.Labels[k]) + "\n"
					}
					_, at := m.bounds()
					if !m.cursor.IsZero() {
						at = m.cursor
					}
					frames := m.history.Snapshots()
					i := sort.Search(len(frames), func(i int) bool { return frames[i].At.After(at) }) - 1
					if values := data.results[item.key]; i >= 0 && i < len(values) {
						value := "N/A"
						if values[i].Value != nil {
							value = fmt.Sprintf("%.12g", *values[i].Value)
						}
						m.detail += "\nTime: " + frames[i].At.Format(time.RFC3339Nano) + "\nValue: " + value + "\nStatus: " + values[i].Status
					} else {
						m.detail += "\nThis series is not plotted. Hide other series to bring it into the first 8."
					}
				}
			}
		}
		m.detailReturn, m.mode, m.detailOffset = m.mode, "detail", 0
		return m, nil
	}
	switch key {
	case "/":
		return m, m.startInput("search", m.filter)
	case "up", "k":
		m.row = max(0, m.row-1)
	case "down", "j":
		m.row = min(max(0, len(items)-1), m.row+1)
	case "pgup":
		m.row = max(0, m.row-max(1, m.height-15))
	case "pgdown":
		m.row = min(max(0, len(items)-1), m.row+max(1, m.height-15))
	case "c":
		if m.mode == "labels" && len(m.cfg.Panels) > 0 {
			m.cfg.Panels[m.focused].Labels = nil
			delete(m.cache, m.focused)
		} else if m.mode == "series" && len(m.cfg.Panels) > 0 {
			m.cfg.Panels[m.focused].Hidden = nil
			delete(m.cache, m.focused)
		} else {
			m.filter = ""
		}
	case "enter", "space":
		if len(items) == 0 {
			return m, nil
		}
		m.row = min(m.row, len(items)-1)
		item := items[m.row]
		switch m.mode {
		case "browser":
			if len(m.cfg.Panels) == 4 {
				m.message = "Four panels are open; Esc then d removes the focused panel"
				return m, nil
			}
			m.cfg.Panels = append(m.cfg.Panels, metrics.Selection{Metric: item.family.Name, View: metrics.DefaultView(item.family)})
			m.focused = len(m.cfg.Panels) - 1
			m.mode = "dashboard"
			m.filter = ""
			m.row = 0
		case "labels":
			p := &m.cfg.Panels[m.focused]
			if p.Labels == nil {
				p.Labels = metrics.Labels{}
			}
			if v, ok := p.Labels[item.key]; ok && v == item.value {
				delete(p.Labels, item.key)
			} else {
				p.Labels[item.key] = item.value
			}
			delete(m.cache, m.focused)
		case "series":
			p := &m.cfg.Panels[m.focused]
			found := -1
			for i, key := range p.Hidden {
				if key == item.key {
					found = i
					break
				}
			}
			if found < 0 {
				p.Hidden = append(p.Hidden, item.key)
			} else {
				p.Hidden = append(p.Hidden[:found], p.Hidden[found+1:]...)
			}
			delete(m.cache, m.focused)
		}
	}
	return m, nil
}

func (m *Model) data(index int) panelData {
	sel := m.cfg.Panels[index]
	signature := selectionSignature(sel)
	if data, ok := m.cache[index]; ok && data.signature == signature {
		return data
	}
	data := buildPanel(m.history.Snapshots(), sel)
	data.signature = signature
	m.cache[index] = data
	return data
}

func (m *Model) bounds() (time.Time, time.Time) {
	end := m.end
	if end.IsZero() {
		end = m.latest.At
	}
	if end.IsZero() {
		end = time.Now()
	}
	return end.Add(-m.window), end
}

func (m *Model) pan(direction int) {
	frames := m.history.Snapshots()
	if len(frames) == 0 {
		return
	}
	_, end := m.bounds()
	end = end.Add(time.Duration(direction) * m.window / 2)
	if end.Before(frames[0].At) {
		end = frames[0].At
	}
	if end.After(frames[len(frames)-1].At) {
		end = frames[len(frames)-1].At
	}
	m.end, m.cursor = end, end
}

func (m *Model) moveCursor(direction int) {
	frames := m.history.Snapshots()
	if len(frames) == 0 {
		return
	}
	start, end := m.bounds()
	at := m.cursor
	if at.IsZero() {
		at = end
	}
	i := sort.Search(len(frames), func(i int) bool { return !frames[i].At.Before(at) })
	i = min(len(frames)-1, i)
	i = max(0, min(len(frames)-1, i+direction))
	m.cursor = frames[i].At
	if m.end.IsZero() {
		m.end = end
	}
	if m.cursor.Before(start) {
		m.end = m.cursor.Add(m.window)
	} else if m.cursor.After(end) {
		m.end = m.cursor
	}
}

// Safe removes control sequences from untrusted endpoint text before rendering.
func Safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}
func fill(s string, width int) string {
	s = fit(s, width)
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		v := tea.NewView("Starting termfana…")
		v.AltScreen = true
		return v
	}
	if m.width < 80 || m.height < 24 {
		v := tea.NewView(fmt.Sprintf("termfana needs an 80×24 terminal (currently %d×%d).\nResize to continue. Sampling continues. Ctrl+C exits.", m.width, m.height))
		v.AltScreen = true
		return v
	}
	badge := accent.Render("LIVE")
	if !m.end.IsZero() {
		badge = warning.Render("HISTORY · still sampling")
	}
	if m.demo {
		badge = warning.Render("DEMO") + "  " + badge
	}
	title := accent.Bold(true).Render(" termfana") + muted.Render(" / metrics in your terminal")
	header := fill(title, m.width-ansi.StringWidth(badge)-2) + badge
	target := muted.Render(" " + Safe(m.cfg.Connection.DisplayURL()))
	status := " Connecting · waiting for first scrape"
	if !m.latest.At.IsZero() {
		frames := m.history.Snapshots()
		span := time.Duration(0)
		if len(frames) > 1 {
			span = frames[len(frames)-1].At.Sub(frames[0].At).Round(time.Second)
		}
		status = fmt.Sprintf(" Every %s · %s · last %s · %d samples / %s · window %s", m.cfg.Interval, m.latest.Duration.Round(time.Millisecond), m.lastSuccess.At.Format("15:04:05 MST"), len(frames), span, m.window)
		if m.latest.Error != "" {
			last := "never"
			if !m.lastSuccess.At.IsZero() {
				last = m.lastSuccess.At.Format("15:04:05")
			}
			status = " Scrape failed: " + Safe(m.latest.Error) + " · last success " + last
		}
	}
	bodyHeight := m.height - 7
	var body string
	if m.help {
		body = m.helpView(bodyHeight)
	} else if m.mode == "detail" {
		lines := strings.Split(ansi.Hardwrap(m.detail, m.width-4, true), "\n")
		available := bodyHeight - 3
		m.detailOffset = min(m.detailOffset, max(0, len(lines)-available))
		visible := []string{accent.Bold(true).Render(" SERIES / METRIC DETAILS"), ""}
		for _, line := range lines[m.detailOffset:min(len(lines), m.detailOffset+available)] {
			visible = append(visible, " "+line)
		}
		for len(visible) < bodyHeight {
			visible = append(visible, "")
		}
		body = strings.Join(visible, "\n")
	} else if m.mode != "dashboard" {
		body = m.menuView(bodyHeight)
	} else {
		body = m.dashboard(bodyHeight)
	}
	footer := " a add   Tab focus   v view   l labels   g series   ←/→ inspect   +/- zoom   [/] pan   Space freeze   r live   s save   ? help"
	if m.mode != "dashboard" {
		footer = " ↑/↓ navigate   / search   Enter select/details   Space toggle   i inspect   c clear   Esc back   ? help   q quit"
	}
	message := " " + m.message
	if m.inputMode != "" {
		label := " Search: "
		if m.inputMode == "save" {
			label = " Save session (overwrites path): "
		}
		message = label + m.input.View()
	}
	content := strings.Join([]string{fit(header, m.width), fit(target, m.width), fit(muted.Render(status), m.width), muted.Render(strings.Repeat("─", m.width)), body, fit(warning.Render(message), m.width), fit(muted.Render(footer), m.width), ""}, "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m *Model) menuView(height int) string {
	items := m.items()
	m.row = max(0, min(m.row, len(items)-1))
	title, subtitle := "METRIC EXPLORER", "Select a metric to plot. Counters open as rates; histograms as p95."
	if m.mode == "labels" {
		title, subtitle = "LABEL FILTERS", "Enter toggles an exact match; different keys are combined with AND. Esc returns."
	}
	if m.mode == "series" {
		title, subtitle = "SERIES & LABELS", "Enter hides/unhides a series. The first 8 eligible series are plotted. Esc returns."
		subtitle = "Enter inspects full labels and values; Space hides/unhides. First 8 eligible series are plotted."
	}
	rows := []string{accent.Bold(true).Render(" "+title) + muted.Render(fmt.Sprintf("  %d matches", len(items))), " " + subtitle, muted.Render(" Filter: " + Safe(m.filter)), ""}
	visible := max(1, height-8)
	start := max(0, min(m.row-visible/2, len(items)-visible))
	for i := start; i < min(len(items), start+visible); i++ {
		item := items[i]
		line := "  " + Safe(item.title)
		if m.mode == "browser" {
			line = fill(line, m.width-30) + fmt.Sprintf(" %-12s %6d series", item.family.Type, item.family.Series)
		} else {
			line = fill(line, m.width-18) + " " + item.detail
		}
		line = fill(line, m.width)
		if i == m.row {
			line = selectedStyle.Render(line)
		} else {
			line = bright.Render(line)
		}
		rows = append(rows, line)
	}
	if len(items) == 0 {
		text := " No matching metrics. Clear the filter with c."
		if m.lastSuccess.At.IsZero() {
			text = " Waiting for a successful scrape…"
		}
		rows = append(rows, muted.Render(text))
	}
	for len(rows) < height-4 {
		rows = append(rows, "")
	}
	if len(items) > 0 {
		item := items[m.row]
		rows = append(rows, muted.Render(strings.Repeat("─", m.width)), fit(" "+Safe(item.title), m.width))
		if m.mode == "browser" {
			rows = append(rows, fit(" "+Safe(item.family.Help), m.width))
		} else {
			rows = append(rows, fit(" "+Safe(item.detail), m.width))
		}
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	for i := range rows {
		rows[i] = fit(rows[i], m.width)
	}
	return strings.Join(rows[:height], "\n")
}

func (m *Model) dashboard(height int) string {
	if len(m.cfg.Panels) == 0 {
		return strings.Repeat("\n", height-1)
	}
	if m.fullscreen || m.width < 120 || height < 28 || len(m.cfg.Panels) == 1 {
		return m.panel(m.focused, m.width, height)
	}
	columns := 2
	rowCount := (len(m.cfg.Panels) + columns - 1) / columns
	panelHeight := height / rowCount
	panelWidth := m.width / 2
	rows := []string{}
	for row := 0; row < rowCount; row++ {
		parts := []string{}
		for col := 0; col < columns; col++ {
			i := row*columns + col
			width := panelWidth
			if col == 1 {
				width = m.width - panelWidth
			}
			if i < len(m.cfg.Panels) {
				parts = append(parts, m.panel(i, width, panelHeight))
			} else {
				parts = append(parts, lipgloss.NewStyle().Width(width).Height(panelHeight).Render(muted.Render("  a  Add another metric")))
			}
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}
	result := lipgloss.JoinVertical(lipgloss.Left, rows...)
	if panelHeight*rowCount < height {
		result += "\n"
	}
	return result
}

func (m *Model) panel(index, width, height int) string {
	p := m.cfg.Panels[index]
	data := m.data(index)
	innerW, innerH := width-4, height-2
	border := lipgloss.Color("#34435A")
	if index == m.focused {
		border = lipgloss.Color("#58D5C9")
	}
	view := p.View
	if view == "rate" {
		view += " /s"
	}
	if strings.HasPrefix(view, "p") {
		view += " ≈"
	}
	if f, ok := metrics.ResolveFamily(m.lastSuccess, p.Metric); ok {
		unit := f.Unit
		if unit == "" && strings.HasSuffix(f.Name, "_seconds") {
			unit = "seconds"
		}
		if unit == "" && strings.HasSuffix(f.Name, "_bytes") {
			unit = "bytes"
		}
		if unit != "" && p.View != "rate" {
			view += " · " + Safe(unit)
		}
	}
	title := fmt.Sprintf("%d  %s", index+1, Safe(p.Metric))
	rows := []string{accent.Bold(true).Render(fit(title, innerW)), muted.Render(fit(view+"  "+Safe(p.Labels.String()), innerW))}
	chartHeight := max(4, innerH-5)
	start, end := m.bounds()
	rows = append(rows, chart.Render(data.series, chart.Options{Width: innerW, Height: chartHeight, Start: start, End: end, Cursor: m.cursor, ASCII: m.cfg.ASCII}))
	frames := m.history.Snapshots()
	at := end
	if !m.cursor.IsZero() {
		at = m.cursor
	}
	pointIndex := sort.Search(len(frames), func(i int) bool { return frames[i].At.After(at) }) - 1
	for i, s := range data.series {
		if i >= 2 {
			break
		}
		value := "N/A"
		status := "missing"
		if pointIndex >= 0 && pointIndex < len(data.results[s.Key]) {
			r := data.results[s.Key][pointIndex]
			status = r.Status
			if r.Value != nil {
				value = chart.Number(*r.Value)
			}
		}
		if status != "ok" {
			value += " " + status
		}
		label := s.Label
		if label == "" {
			label = s.Key
		}
		label = fmt.Sprintf("%d %s", i+1, Safe(label))
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(chart.Palette[i]))
		rows = append(rows, style.Render(fill(label, max(5, innerW-len(value)-2))+"  "+value))
	}
	for i := min(len(data.series), 2); i < 2; i++ {
		rows = append(rows, "")
	}
	info := fmt.Sprintf("%d/%d series · g inspect", len(data.series), data.total)
	if !m.cursor.IsZero() {
		info = m.cursor.Format("15:04:05.000") + " · " + info
	}
	if err := metrics.ValidateSelection(m.lastSuccess, p); err != nil && !m.lastSuccess.At.IsZero() {
		info = err.Error()
	}
	rows = append(rows, muted.Render(fit(info, innerW)))
	content := strings.Join(rows, "\n")
	// Limit both dimensions so small windows and long labels cannot corrupt layout.
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = fit(lines[i], innerW)
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m *Model) helpView(height int) string {
	lines := []string{
		" KEYBOARD GUIDE", "", " a /       Browse metrics / search", " Enter     Add a metric, or maximize the focused panel", " Tab 1–4   Switch panels", " d         Remove the focused panel", " v         Cycle raw / rate / histogram views", " l         Choose exact label filters", " g         Inspect complete series labels and hide/unhide curves", " ← →       Inspect samples with a synchronized cursor", " + -       Zoom the shared time window", " [ ]       Move backward / forward in retained history", " Space     Freeze the view; sampling continues", " r / Home  Return to live data", " s         Save connection and panels as a session JSON file", " --ascii   Use ASCII curves (numbered 1–8)", "", " Rates and histogram views need two consecutive valid samples.", " Gaps and resets show N/A. p50/p95/p99 are bucket estimates.", " Sessions contain configuration only; history stays in memory.", "", " ? / Esc   Close help                  q / Ctrl+C   Quit",
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return bright.Render(strings.Join(lines, "\n"))
}
