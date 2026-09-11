package views

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

const (
	logTailBytes = 64 << 10 // how much history to show when a file is opened
	logReadMax   = 1 << 20  // most bytes read per poll
	logMaxLines  = 5000
	logPollEvery = time.Second
)

type logFile struct{ name, path string }

type logPollMsg struct{ gen int }

type logChunkMsg struct {
	gen     int
	lines   []string
	offset  int64
	partial string
	err     error
}

// LogsModel tails the log files of the active bench.
type LogsModel struct {
	bench   core.ActiveBench
	files   []logFile
	active  int
	gen     int    // bumped whenever the tailed file changes; stale reads are dropped
	offset  int64  // next byte to read; -1 starts from the tail
	partial string // unterminated last line
	lines   []string
	err     string

	following bool
	filtering bool
	filter    textinput.Model
	vp        viewport.Model
	visible   bool
	polling   bool
	width     int
	height    int
}

// NewLogsModel creates the log viewer for bench.
func NewLogsModel(bench core.ActiveBench) LogsModel {
	vp := viewport.New(80, 20)
	vp.KeyMap = viewport.KeyMap{
		Up:           key.NewBinding(key.WithKeys("up", "k")),
		Down:         key.NewBinding(key.WithKeys("down", "j")),
		PageUp:       key.NewBinding(key.WithKeys("pgup")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
	}
	fi := textinput.New()
	fi.Placeholder = "text to match"
	fi.CharLimit = 80
	fi.Width = 30
	return LogsModel{bench: bench, files: discoverLogs(bench.Path), offset: -1, following: true, vp: vp, filter: fi}
}

// discoverLogs lists *.log files in $VYBENCH_LOG (the Homebrew service logs)
// and the bench's logs/ directory, service logs first.
func discoverLogs(benchPath string) []logFile {
	var dirs []string
	if d := os.Getenv("VYBENCH_LOG"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, filepath.Join(benchPath, "logs"))

	seen := map[string]bool{}
	names := map[string]int{}
	var files []logFile
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, "*.log"))
		for _, p := range matches {
			real, err := filepath.EvalSymlinks(p)
			if err != nil || seen[real] {
				continue
			}
			seen[real] = true
			names[filepath.Base(p)]++
			files = append(files, logFile{name: filepath.Base(p), path: p})
		}
	}
	for i, f := range files {
		if names[f.name] > 1 && strings.HasPrefix(f.path, filepath.Join(benchPath, "logs")) {
			files[i].name = "bench/" + f.name
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := logRank(files[i].name), logRank(files[j].name)
		if ri != rj {
			return ri < rj
		}
		return files[i].name < files[j].name
	})
	return files
}

func logRank(name string) int {
	for i, p := range []string{"web", "worker", "scheduler", "socketio", "supervisor", "frappe"} {
		if strings.HasPrefix(name, p) {
			return i
		}
	}
	return 99
}

// CapturingInput reports whether the filter box owns the keyboard.
func (m LogsModel) CapturingInput() bool { return m.filtering }

// SetSize sets the content area.
func (m *LogsModel) SetSize(w, h int) {
	m.width, m.height = w, h
	m.vp.Width = max(w-2, 10)
	m.vp.Height = max(h-5, 3)
	m.render()
}

// SetVisible is called on tab changes; files are only polled while visible.
func (m *LogsModel) SetVisible(v bool) tea.Cmd {
	was := m.visible
	m.visible = v
	if !v || was {
		return nil
	}
	current := ""
	if m.active < len(m.files) {
		current = m.files[m.active].path
	}
	m.files = discoverLogs(m.bench.Path)
	m.active = 0
	for i, f := range m.files {
		if f.path == current {
			m.active = i
		}
	}
	if len(m.files) > 0 && m.files[m.active].path == current && m.offset >= 0 {
		if m.polling {
			return nil
		}
		m.polling = true
		return m.read() // resume where we left off
	}
	return m.restart()
}

// restart begins tailing the selected file from its last 64 KiB.
func (m *LogsModel) restart() tea.Cmd {
	m.gen++
	m.lines, m.partial, m.offset, m.err = nil, "", -1, ""
	m.following = true
	m.render()
	if !m.visible || len(m.files) == 0 {
		m.polling = false
		return nil
	}
	m.polling = true
	return m.read()
}

func (m LogsModel) read() tea.Cmd {
	return readLog(m.gen, m.files[m.active].path, m.offset, m.partial)
}

func readLog(gen int, path string, offset int64, partial string) tea.Cmd {
	return func() tea.Msg {
		msg := logChunkMsg{gen: gen, offset: offset, partial: partial}
		f, err := os.Open(path)
		if err != nil {
			msg.err = err
			return msg
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			msg.err = err
			return msg
		}
		size := st.Size()
		fromTail := offset < 0
		if fromTail {
			offset, partial = max(0, size-logTailBytes), ""
		}
		if size < offset { // truncated or rotated: start again from the top
			offset, partial = 0, ""
		}
		msg.offset, msg.partial = offset, partial
		if size == offset {
			return msg
		}
		buf := make([]byte, min(size-offset, logReadMax))
		n, _ := f.ReadAt(buf, offset)
		parts := strings.Split(partial+string(buf[:n]), "\n")
		msg.partial = parts[len(parts)-1]
		msg.lines = parts[:len(parts)-1]
		if fromTail && offset > 0 && len(msg.lines) > 0 {
			msg.lines = msg.lines[1:] // began mid-line
		}
		if len(msg.partial) > logTailBytes {
			msg.lines, msg.partial = append(msg.lines, msg.partial), ""
		}
		msg.offset = offset + int64(n)
		return msg
	}
}

func (m LogsModel) Update(msg tea.Msg) (LogsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case logChunkMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.offset, m.partial = msg.offset, msg.partial
		m.err = ""
		if msg.err != nil {
			m.err = msg.err.Error()
		}
		if len(msg.lines) > 0 || msg.err != nil {
			for _, l := range msg.lines {
				m.lines = append(m.lines, ansi.Strip(strings.TrimRight(l, "\r")))
			}
			if over := len(m.lines) - logMaxLines; over > 0 {
				m.lines = append([]string(nil), m.lines[over:]...)
			}
			m.render()
		}
		if !m.visible {
			m.polling = false
			return m, nil
		}
		gen := m.gen
		return m, tea.Tick(logPollEvery, func(time.Time) tea.Msg { return logPollMsg{gen: gen} })

	case logPollMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if !m.visible || len(m.files) == 0 {
			m.polling = false
			return m, nil
		}
		return m, m.read()

	case BenchSwitchedMsg:
		m.bench = msg.Bench
		m.files = discoverLogs(m.bench.Path)
		m.active = 0
		cmd := m.restart()
		return m, cmd

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.following = m.vp.AtBottom()
		return m, cmd

	case tea.KeyMsg:
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filter.SetValue("")
				fallthrough
			case "enter":
				m.filtering = false
				m.filter.Blur()
				m.render()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.render()
			return m, cmd
		}
		switch msg.String() {
		case "left", "h", "right", "l":
			if len(m.files) < 2 {
				return m, nil
			}
			delta := 1
			if k := msg.String(); k == "left" || k == "h" {
				delta = -1
			}
			m.active = (m.active + delta + len(m.files)) % len(m.files)
			cmd := m.restart()
			return m, cmd
		case "r":
			m.files = discoverLogs(m.bench.Path)
			m.active = 0
			cmd := m.restart()
			return m, cmd
		case "f":
			m.following = !m.following
			if m.following {
				m.vp.GotoBottom()
			}
			return m, nil
		case "G", "end":
			m.following = true
			m.vp.GotoBottom()
			return m, nil
		case "g", "home":
			m.following = false
			m.vp.GotoTop()
			return m, nil
		case "/":
			m.filtering = true
			cmd := m.filter.Focus()
			return m, cmd
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if !m.vp.AtBottom() {
			m.following = false
		}
		return m, cmd
	}
	return m, nil
}

// render rebuilds the viewport from the buffered lines and the filter.
func (m *LogsModel) render() {
	q := strings.ToLower(m.filter.Value())
	var out []string
	for _, l := range m.lines {
		if q == "" || strings.Contains(strings.ToLower(l), q) {
			out = append(out, colorLogLine(l))
		}
	}
	switch {
	case len(m.files) == 0:
		out = []string{theme.StyleMuted.Render("No log files found in " + strings.Join(m.logDirs(), " or ") + ".")}
		if hint := journalHint(); hint != "" {
			out = append(out, "", theme.StyleMuted.Render(hint))
		}
	case m.err != "":
		out = append(out, theme.StyleLogError.Render(m.err))
	case len(out) == 0 && q != "":
		out = []string{theme.StyleMuted.Render("No lines match \"" + m.filter.Value() + "\".")}
	case len(out) == 0:
		out = []string{theme.StyleMuted.Render("(empty — waiting for output)")}
	}
	m.vp.SetContent(strings.Join(out, "\n"))
	if m.following {
		m.vp.GotoBottom()
	}
}

func (m LogsModel) logDirs() []string {
	var dirs []string
	if d := os.Getenv("VYBENCH_LOG"); d != "" {
		dirs = append(dirs, d)
	}
	return append(dirs, filepath.Join(m.bench.Path, "logs"))
}

// journalHint points at the journal where snap and systemd services log.
func journalHint() string {
	switch core.DetectPlatform() {
	case core.PlatformSnap:
		return "The snap's services log to the systemd journal: sudo snap logs -f " + core.InstanceName()
	case core.PlatformNative:
		return "The services log to the systemd journal: journalctl -u frappe-web -f"
	}
	return ""
}

func (m LogsModel) View(w, h int) string {
	var tabs []string
	for i, f := range m.files {
		if i == m.active {
			tabs = append(tabs, theme.StyleTabActive.Render(f.name))
		} else {
			tabs = append(tabs, theme.StyleTabInactive.Render(f.name))
		}
	}
	tabBar := fit(lipgloss.JoinHorizontal(lipgloss.Top, tabs...), w, 1)

	follow := theme.StyleBadgeStopped.Render("OFF")
	if m.following {
		follow = theme.StyleBadgeRunning.Render("ON")
	}
	parts := []string{theme.StyleMuted.Render("Follow: ") + follow}
	if len(m.files) > 0 {
		parts = append(parts, theme.StyleMuted.Render(truncateLeft(m.files[m.active].path, max(w/2, 20))))
	}
	parts = append(parts, theme.StyleMuted.Render(fmt.Sprintf("%d lines", len(m.lines))))
	if m.filtering || m.filter.Value() != "" {
		parts = append(parts, "Filter: "+m.filter.View())
	}
	status := " " + strings.Join(parts, theme.StyleMuted.Render("  │  "))

	return lipgloss.JoinVertical(lipgloss.Left,
		tabBar,
		panel(w, h-3, m.vp.View()),
		fit(status, w, 1),
		helpBar(w, "←→", "file", "f", "follow", "/", "filter", "↑↓/PgUp/PgDn", "scroll", "g/G", "top/bottom", "r", "rescan"),
	)
}

func colorLogLine(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "error") || strings.Contains(l, "exception") || strings.Contains(l, "traceback"):
		return theme.StyleLogError.Render(line)
	case strings.Contains(l, "warn"):
		return theme.StyleLogWarn.Render(line)
	case strings.Contains(l, "success") || strings.Contains(l, "started") || strings.Contains(l, " 200 "):
		return theme.StyleLogOK.Render(line)
	}
	return line
}
