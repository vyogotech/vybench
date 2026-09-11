// Package views — the tabs of the vybench TUI and the widgets they share.
package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

// panel renders content in a bordered box exactly w columns wide and h rows
// tall. Content is clipped rather than wrapped, so a long line can never push
// the layout past the terminal.
func panel(w, h int, content string) string {
	w, h = max(w, 4), max(h, 3)
	return theme.StylePanel.Width(w - 2).Height(h - 2).Render(fit(content, w-2, h-2))
}

// fit clips every line of s to w cells and returns exactly h lines.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlay draws top centred over base, which is w×h, keeping the parts of
// base to the left and right of it.
func overlay(base, top string, w, h int) string {
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	topLines := strings.Split(top, "\n")
	if len(topLines) > h && h > 4 {
		// Too tall for the screen: drop rows from the middle, keeping the
		// bottom (its keys and border) visible.
		topLines = append(topLines[:h-2:h-2], topLines[len(topLines)-2:]...)
	}
	tw := lipgloss.Width(top)
	x := max(0, (w-tw)/2)
	y := max(0, (h-len(topLines))/2)
	for i, tl := range topLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		bl := baseLines[row]
		if bw := ansi.StringWidth(bl); bw < x {
			bl += strings.Repeat(" ", x-bw)
		}
		left := ansi.Truncate(bl, x, "")
		right := ansi.TruncateLeft(bl, x+ansi.StringWidth(tl), "")
		baseLines[row] = left + "\x1b[0m" + tl + "\x1b[0m" + right
	}
	return strings.Join(baseLines, "\n")
}

func ansiStrip(s string) string { return ansi.Strip(s) }

// truncateLeft keeps the end of s, which is the informative part of a path.
func truncateLeft(s string, width int) string {
	w := ansi.StringWidth(s)
	if w <= width || width < 2 {
		return s
	}
	return "…" + ansi.TruncateLeft(s, w-width+1, "")
}

// helpLine renders key/label pairs: helpLine("n", "new", "d", "drop").
func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, theme.StyleHelpKey.Render("["+pairs[i]+"]")+" "+theme.StyleMuted.Render(pairs[i+1]))
	}
	return " " + strings.Join(parts, "  ")
}

// helpBar is a helpLine clipped to w, so it can never widen the view it is
// joined to (lipgloss.JoinVertical pads every row to the widest one).
func helpBar(w int, pairs ...string) string { return fit(helpLine(pairs...), w, 1) }

// formLabelW is the label column of a form row.
const formLabelW = 15

// formRow renders one form field on a single line: "▶ Label     value".
func formRow(focused bool, text, value string) string {
	if focused {
		return theme.StylePrimary.Render(fmt.Sprintf("▶ %-*s", formLabelW, text)) + " " + value
	}
	return theme.StyleMuted.Render(fmt.Sprintf("  %-*s", formLabelW, text)) + " " + value
}

// formNote renders a line under a field, aligned with the values.
func formNote(s string) string { return strings.Repeat(" ", formLabelW+3) + s }

// dialog frames a form: title, blank line, rows, blank line, keys.
func dialog(title string, rows []string, keys string) string {
	body := append([]string{theme.StyleBold.Render(title), ""}, rows...)
	body = append(body, "", keys)
	return theme.StyleDialog.Render(strings.Join(body, "\n"))
}

// errorText renders a form error wrapped to a dialog-friendly width.
func errorText(msg string) string {
	lines := strings.Split(lipgloss.NewStyle().Width(58).Render("✖ "+msg), "\n")
	for i, l := range lines {
		lines[i] = theme.StyleLogError.Render(strings.TrimRight(l, " "))
	}
	return strings.Join(lines, "\n")
}

// ── Scrolling lists ─────────────────────────────────────────────────────────

// listNav is a cursor over a list that shows rows lines at a time.
type listNav struct{ cursor, offset int }

// fix clamps the cursor to n items and scrolls so it stays visible.
func (l *listNav) fix(n, rows int) {
	rows = max(rows, 1)
	if n <= 0 {
		l.cursor, l.offset = 0, 0
		return
	}
	l.cursor = min(max(l.cursor, 0), n-1)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+rows {
		l.offset = l.cursor - rows + 1
	}
	l.offset = max(0, min(l.offset, n-rows))
}

// key applies a navigation key and reports whether it was one.
func (l *listNav) key(k string, n, rows int) bool {
	switch k {
	case "up", "k":
		l.cursor--
	case "down", "j":
		l.cursor++
	case "pgup":
		l.cursor -= max(rows, 1)
	case "pgdown":
		l.cursor += max(rows, 1)
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = n - 1
	default:
		return false
	}
	l.fix(n, rows)
	return true
}

// window returns the [start, end) slice of n items to draw in rows lines.
func (l listNav) window(n, rows int) (int, int) {
	start := min(l.offset, max(n-1, 0))
	return start, min(n, start+max(rows, 1))
}

// scrollHint renders "12-20 of 57" when a list does not fit.
func scrollHint(start, end, n int) string {
	if start == 0 && end >= n {
		return ""
	}
	return theme.StyleMuted.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, n))
}

// ── Commands ────────────────────────────────────────────────────────────────

// openURL opens url in the browser, or tells the user where to go when no
// browser can be launched (e.g. inside the snap on a headless server).
func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := core.OpenURLCommand(url)
		if err := cmd.Start(); err != nil {
			return StatusMsg{Text: "No browser available: open " + url + " yourself", Err: true}
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				return StatusMsg{Text: fmt.Sprintf("Opening a browser failed (%v): open %s yourself", err, url), Err: true}
			}
		case <-ctx.Done():
		}
		return StatusMsg{Text: "Opened " + url}
	}
}

// ── Database reachability ───────────────────────────────────────────────────

type dbState int

const (
	dbUnknown dbState = iota
	dbUp
	dbDown
)

// dbCheckMsg reports whether a bench's database server accepts connections.
type dbCheckMsg struct {
	bench, engine string
	up            bool
	why           string // what is in the way, when it is down
}

func (m dbCheckMsg) state() dbState {
	if m.up {
		return dbUp
	}
	return dbDown
}

// dbStartedMsg is broadcast when a start-the-database job succeeds, so open
// dialogs check again.
type dbStartedMsg struct{}

// checkDB probes the database off the UI thread. bench may be "".
func checkDB(bench, engine string) tea.Cmd {
	return func() tea.Msg {
		up, why := core.DatabaseStatus(bench, engine)
		return dbCheckMsg{bench: bench, engine: engine, up: up, why: why}
	}
}

// dbLine renders a database status; startKey mentions Ctrl+S when it is down.
func dbLine(s dbState, engine string, startKey bool) string {
	name := core.DBLabel(engine)
	switch s {
	case dbUp:
		return theme.StyleLogOK.Render("✔ " + name + " is running")
	case dbDown:
		line := theme.StyleLogError.Render("✖ " + name + " is not running")
		if startKey {
			line += "  " + theme.StyleHelpKey.Render("[Ctrl+S]") + theme.StyleMuted.Render(" start it")
		}
		return line
	}
	return theme.StyleMuted.Render("checking " + name + "…")
}

// startDBJob runs the steps that start a bench's database, leaving the open
// dialog in place underneath.
func startDBJob(sup *core.Supervisor, bench, engine string) tea.Cmd {
	if sup == nil {
		return setError(core.ErrUnsupportedPlatform)
	}
	steps, err := sup.StartDatabaseSteps(bench, engine)
	if err != nil {
		return setError(err)
	}
	name := core.DBLabel(engine)
	return runJob("Start "+name, core.JobSpec{Steps: steps}, name+" is running. Press Esc to go back to the form.",
		dbStartedMsg{}, ServicesChangedMsg{})
}

// dbRows is the Server row of a dialog, plus what is in the way when the
// database is down.
func dbRows(s dbState, why, engine string) []string {
	rows := []string{formRow(false, "Server", dbLine(s, engine, true))}
	if s == dbDown && why != "" {
		rows = append(rows, formNotes(theme.StyleBadgeWarn, why)...)
	}
	return rows
}

func checkbox(on bool) string {
	if on {
		return theme.StylePrimary.Render("[x]")
	}
	return theme.StyleMuted.Render("[ ]")
}

// formNotes wraps text under a form field.
func formNotes(style lipgloss.Style, text string) []string {
	var out []string
	for _, l := range strings.Split(lipgloss.NewStyle().Width(46).Render(text), "\n") {
		out = append(out, formNote(style.Render(strings.TrimRight(l, " "))))
	}
	return out
}

// Overlay draws top centred over base (w×h). Exported for the root model.
func Overlay(base, top string, w, h int) string { return overlay(base, top, w, h) }

// Fit clips s to w columns and exactly h lines. Exported for the root model.
func Fit(s string, w, h int) string { return fit(s, w, h) }
