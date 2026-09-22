package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

// maxJobLines bounds the output a job keeps; bench init produces tens of
// thousands of lines.
const maxJobLines = 5000

type jobLinesMsg struct {
	id    int
	lines []string
}

type jobDoneMsg struct {
	id  int
	err error
}

// jobSeq tags each job's messages so stale ones from a closed job are
// ignored. It is only touched from Update, which Bubble Tea never runs
// concurrently.
var jobSeq int

// JobModel is the overlay that runs a core.Job and shows its output.
type JobModel struct {
	id        int
	title     string
	job       *core.Job
	lines     []string
	done      bool
	err       error
	success   string
	after     []tea.Msg
	failed    []tea.Msg
	hint      string
	diagnosis string
	scroll    int // lines scrolled up from the bottom
	spin      spinner.Model
	width     int
	height    int
}

// NewJob starts msg's job and returns the overlay showing it.
func NewJob(msg RunJobMsg) (JobModel, tea.Cmd) {
	jobSeq++
	m := JobModel{
		id:      jobSeq,
		title:   msg.Title,
		success: msg.Success,
		after:   msg.After,
		failed:  msg.Failed,
		hint:    msg.FailHint,
		spin:    spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(theme.StyleBadgeWarn)),
		job:     core.StartJob(msg.Spec),
	}
	return m, tea.Batch(waitJob(m.id, m.job), m.spin.Tick)
}

// waitJob delivers the job's next batch of output, or its result once the
// output channel is closed.
func waitJob(id int, j *core.Job) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-j.Lines()
		if !ok {
			return jobDoneMsg{id: id, err: j.Err()}
		}
		lines := []string{line}
		for len(lines) < 200 {
			select {
			case l, ok := <-j.Lines():
				if !ok {
					return jobLinesMsg{id: id, lines: lines}
				}
				lines = append(lines, l)
			default:
				return jobLinesMsg{id: id, lines: lines}
			}
		}
		return jobLinesMsg{id: id, lines: lines}
	}
}

// Active reports whether the overlay is showing.
func (m JobModel) Active() bool { return m.job != nil }

// Running reports whether the job is still running.
func (m JobModel) Running() bool { return m.job != nil && !m.done }

// Err is the job's result. It stays nil until the job goroutine finishes.
func (m JobModel) Err() error {
	if m.job == nil {
		return nil
	}
	return m.job.Err()
}

// Cancel stops a running job.
func (m JobModel) Cancel() {
	if m.Running() {
		m.job.Cancel()
	}
}

// SetSize sets the area the overlay is drawn over.
func (m *JobModel) SetSize(w, h int) { m.width, m.height = w, h }

func (m JobModel) outputRows() int { return max(m.height-9, 3) }

func (m JobModel) Update(msg tea.Msg) (JobModel, tea.Cmd) {
	switch msg := msg.(type) {
	case jobLinesMsg:
		if msg.id != m.id {
			return m, nil
		}
		m.lines = core.CollapseProgress(m.lines, msg.lines)
		if over := len(m.lines) - maxJobLines; over > 0 {
			m.lines = append([]string(nil), m.lines[over:]...)
		}
		if m.scroll > 0 {
			m.scroll += len(msg.lines) // keep the view still while scrolled back
		}
		return m, waitJob(m.id, m.job)

	case jobDoneMsg:
		if msg.id != m.id {
			return m, nil
		}
		m.done, m.err = true, msg.err
		switch {
		case msg.err == nil:
			cmds := []tea.Cmd{setStatus(firstNonEmpty(m.success, m.title+": done"))}
			for _, a := range m.after {
				cmds = append(cmds, emit(a))
			}
			return m, tea.Batch(cmds...)
		}
		text := m.title + " failed: " + msg.err.Error()
		if msg.err == core.ErrCancelled {
			text = m.title + ": cancelled"
		} else if why := core.Diagnose(m.lines); why != "" {
			// The cause from the output replaces the generic hint: retrying
			// is pointless until, say, the database is running.
			m.diagnosis = why
			text = m.title + " failed. " + why
		} else if m.hint != "" {
			text += ". " + m.hint
		}
		cmds := []tea.Cmd{emit(StatusMsg{Text: text, Err: true})}
		for _, f := range m.failed {
			cmds = append(cmds, emit(f))
		}
		return m, tea.Batch(cmds...)

	case spinner.TickMsg:
		if m.done {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		rows := m.outputRows()
		maxScroll := max(len(m.lines)-rows, 0)
		switch msg.String() {
		case "up", "k":
			m.scroll++
		case "down", "j":
			m.scroll--
		case "pgup":
			m.scroll += rows
		case "pgdown":
			m.scroll -= rows
		case "home", "g":
			m.scroll = maxScroll
		case "end", "G":
			m.scroll = 0
		case "x", "ctrl+c":
			m.Cancel()
		case "esc", "enter", "q":
			if m.done {
				return JobModel{}, nil
			}
		}
		m.scroll = min(max(m.scroll, 0), maxScroll)
	}
	return m, nil
}

func (m JobModel) View() string {
	w := max(m.width-4, 30)
	rows := m.outputRows()

	var status string
	switch {
	case !m.done:
		status = m.spin.View() + theme.StyleBadgeWarn.Render(" running…")
	case m.err == nil:
		status = theme.StyleLogOK.Render("✔ done")
	case m.err == core.ErrCancelled:
		status = theme.StyleBadgeWarn.Render("■ cancelled")
	default:
		status = theme.StyleLogError.Render("✖ " + m.err.Error())
		if m.diagnosis != "" {
			status += "\n" + theme.StyleBadgeWarn.Render("→ "+m.diagnosis)
		}
	}

	end := len(m.lines) - m.scroll
	start := max(end-rows, 0)
	var out []string
	for _, l := range m.lines[start:end] {
		out = append(out, colorOutput(l))
	}
	for len(out) < rows {
		out = append(out, "")
	}

	var keys string
	if m.done {
		keys = helpLine("Esc", "close", "↑↓/PgUp/PgDn", "scroll")
	} else {
		keys = helpLine("x", "cancel", "↑↓/PgUp/PgDn", "scroll")
	}
	pos := ""
	if m.scroll > 0 {
		pos = theme.StyleMuted.Render(fmt.Sprintf("  (scrolled back %d lines — End to follow)", m.scroll))
	}

	body := strings.Join([]string{
		theme.StyleBold.Render(m.title) + "  " + status,
		theme.StyleMuted.Render(strings.Repeat("─", w-6)),
		fit(strings.Join(out, "\n"), w-6, rows),
		"",
		keys + pos,
	}, "\n")
	return theme.StyleModal.Width(w - 2).Render(fit(body, w-6, rows+4))
}

// colorOutput highlights the step and command lines a Job emits, and errors.
func colorOutput(line string) string {
	switch {
	case strings.HasPrefix(line, "▶ "):
		return theme.StylePrimary.Render(line)
	case strings.HasPrefix(line, "$ "):
		return theme.StyleAccent.Render(line)
	}
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "error") || strings.Contains(l, "traceback") || strings.Contains(l, "failed"):
		return theme.StyleLogError.Render(line)
	case strings.Contains(l, "warn"):
		return theme.StyleLogWarn.Render(line)
	}
	return line
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
