package views

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

type benchForm struct {
	name, version textinput.Model
	engine        string
	focus         int // 0 name, 1 engine, 2 version
	db            dbState
	err           string
}

type attachForm struct {
	name, path textinput.Model
	focus      int
	err        string
}

// BenchesModel lists, switches, creates, attaches and drops benches.
type BenchesModel struct {
	mgr       *core.Manager
	benches   []core.BenchInfo
	isCurrent map[string]bool  // bench name → is this session's bench, resolved on reload
	current   core.ActiveBench // the bench this session operates on
	packaged  string           // Frappe release shipped with the package
	loadErr   error
	nav       listNav
	width     int
	height    int

	form     *benchForm
	attach   *attachForm
	dropping string
}

// NewBenchesModel creates the view and loads the registry.
func NewBenchesModel(mgr *core.Manager, current core.ActiveBench) BenchesModel {
	m := BenchesModel{mgr: mgr, current: current, packaged: mgr.PackagedFrappeVersion()}
	m.reload()
	return m
}

func (m *BenchesModel) reload() {
	m.benches, m.loadErr = m.mgr.ListBenches()
	m.isCurrent = make(map[string]bool, len(m.benches))
	for _, b := range m.benches {
		m.isCurrent[b.Name] = core.SamePath(b.Entry.Path, m.current.Path)
	}
	m.nav.fix(len(m.benches), m.listRows())
}

// CapturingInput reports whether a dialog owns the keyboard.
func (m BenchesModel) CapturingInput() bool {
	return m.form != nil || m.attach != nil || m.dropping != ""
}

// SetSize sets the content area.
func (m *BenchesModel) SetSize(w, h int) {
	m.width, m.height = w, h
	m.nav.fix(len(m.benches), m.listRows())
}

func (m BenchesModel) listRows() int { return max(m.height-8, 1) }

func (m BenchesModel) Update(msg tea.Msg) (BenchesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case BenchSwitchedMsg:
		m.current = msg.Bench
		m.reload()
		return m, nil
	case BenchesChangedMsg, SitesChangedMsg:
		m.reload()
		return m, nil
	case dbCheckMsg:
		if m.form != nil && msg.bench == "" && msg.engine == m.form.engine {
			m.form.db = msg.state()
		}
		return m, nil
	case tea.KeyMsg:
		switch {
		case m.form != nil:
			return m.updateForm(msg)
		case m.attach != nil:
			return m.updateAttach(msg)
		case m.dropping != "":
			return m.updateDrop(msg)
		}
		if m.nav.key(msg.String(), len(m.benches), m.listRows()) {
			return m, nil
		}
		switch msg.String() {
		case "enter", " ":
			if m.nav.cursor < len(m.benches) {
				return m, m.switchTo(m.benches[m.nav.cursor])
			}
		case "n":
			m.form = m.newForm()
		case "a":
			m.attach = m.newAttachForm()
		case "d":
			if m.nav.cursor < len(m.benches) {
				m.dropping = m.benches[m.nav.cursor].Name
			}
		case "r":
			m.reload()
		}
	}
	return m, nil
}

func (m BenchesModel) switchTo(b core.BenchInfo) tea.Cmd {
	if b.Missing {
		return setError(fmt.Errorf("bench %q points at %s, which no longer exists", b.Name, b.Entry.Path))
	}
	if m.isCurrent[b.Name] && b.IsActive {
		return setStatus(b.Name + " is already the active bench")
	}
	if err := m.mgr.SwitchBench(b.Name); err != nil {
		return setError(err)
	}
	ab, err := m.mgr.Bench(b.Name)
	if err != nil {
		return setError(err)
	}
	return tea.Batch(
		emit(BenchSwitchedMsg{Bench: ab}),
		setStatus(fmt.Sprintf("Switched to %s. Running services still serve the old bench until restarted: [r] on Overview, or %s", b.Name, core.RestartHint())),
	)
}

// ── New bench ───────────────────────────────────────────────────────────────

func (m BenchesModel) newForm() *benchForm {
	f := &benchForm{
		name:    newInput("bench-name", 40, false),
		version: newInput("16, 15, develop, v16.3.0…", 40, false),
		engine:  "mariadb",
	}
	if m.packaged != "" {
		f.version.SetValue(core.MajorVersion(m.packaged))
	}
	f.name.Focus()
	return f
}

func (f *benchForm) setFocus(i int) {
	f.focus = min(max(i, 0), 2)
	f.name.Blur()
	f.version.Blur()
	switch f.focus {
	case 0:
		f.name.Focus()
	case 2:
		f.version.Focus()
	}
}

func (m BenchesModel) updateForm(msg tea.KeyMsg) (BenchesModel, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc":
		m.form = nil
		return m, nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return m, nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return m, nil
	case "enter":
		if f.focus < 2 {
			f.setFocus(f.focus + 1)
			return m, nil
		}
		return m.submitForm()
	}
	if f.focus == 1 {
		prev := f.engine
		switch msg.String() {
		case "left", "right", " ", "h", "l":
			f.engine = map[string]string{"mariadb": "postgres", "postgres": "mariadb"}[f.engine]
		case "m":
			f.engine = "mariadb"
		case "p":
			f.engine = "postgres"
		}
		if f.engine != prev {
			f.db = dbUnknown
			return m, checkDB("", f.engine)
		}
		return m, nil
	}
	var cmd tea.Cmd
	if f.focus == 0 {
		f.name, cmd = f.name.Update(msg)
	} else {
		f.version, cmd = f.version.Update(msg)
	}
	f.err = ""
	return m, cmd
}

func (m BenchesModel) submitForm() (BenchesModel, tea.Cmd) {
	f := m.form
	plan, err := m.mgr.PlanBench(core.NewBenchOptions{
		Name:          strings.TrimSpace(f.name.Value()),
		DBEngine:      f.engine,
		FrappeVersion: strings.TrimSpace(f.version.Value()),
	})
	if err != nil {
		f.err = err.Error()
		return m, nil
	}
	if !plan.NeedsInit {
		m.form = nil
		if err := m.mgr.CreateLinked(plan); err != nil {
			return m, setError(err)
		}
		return m, tea.Batch(emit(BenchesChangedMsg{}), setStatus(fmt.Sprintf(
			"Created %s on the packaged Frappe %s. Select it and press Enter to switch.", plan.Name, plan.FrappeVersion)))
	}
	spec, err := m.mgr.InitBenchSpec(plan, m.current.Path)
	if err != nil {
		f.err = err.Error() // keep the form open with the reason
		return m, nil
	}
	m.form = nil
	return m, runJob("Build bench "+plan.Name, spec,
		fmt.Sprintf("Built %s (Frappe %s). Select it and press Enter to switch.", plan.Name, plan.Branch),
		BenchesChangedMsg{})
}

// planPreview says how the version in the form will be built.
func (m BenchesModel) planPreview(version string) []string {
	v := strings.TrimSpace(version)
	if m.packaged != "" && (v == "" || core.SameRelease(v, m.packaged)) {
		return []string{theme.StyleLogOK.Render("✔ Links the packaged Frappe " + m.packaged + ": ready in seconds")}
	}
	if v == "" {
		return []string{theme.StyleMuted.Render("Enter a Frappe version")}
	}
	var out []string
	if m.packaged == "" {
		out = append(out, theme.StyleMuted.Render("No packaged Frappe found for "+core.InstanceName()+"."))
	}
	return append(out,
		theme.StyleBadgeWarn.Render("⚠ Builds Frappe "+core.FrappeBranch(v)+" with bench init. It"),
		theme.StyleBadgeWarn.Render("  downloads Frappe, takes minutes, and needs a Python"),
		theme.StyleBadgeWarn.Render("  and Node that this release supports."),
	)
}

// ── Attach ──────────────────────────────────────────────────────────────────

func (m BenchesModel) newAttachForm() *attachForm {
	f := &attachForm{name: newInput("bench-name", 40, false), path: newInput("/path/to/frappe-bench", 256, false)}
	if !m.current.Registered {
		f.path.SetValue(m.current.Path)
	} else if cwd, err := os.Getwd(); err == nil && core.IsBenchDir(cwd) {
		f.path.SetValue(cwd)
	}
	f.name.Focus()
	return f
}

func (m BenchesModel) updateAttach(msg tea.KeyMsg) (BenchesModel, tea.Cmd) {
	f := m.attach
	switch msg.String() {
	case "esc":
		m.attach = nil
		return m, nil
	case "tab", "down", "shift+tab", "up":
		f.focus = 1 - f.focus
	case "enter":
		if f.focus == 0 {
			f.focus = 1
		} else {
			name := strings.TrimSpace(f.name.Value())
			if err := m.mgr.AttachBench(name, strings.TrimSpace(f.path.Value()), "", ""); err != nil {
				f.err = err.Error()
				return m, nil
			}
			m.attach = nil
			return m, tea.Batch(emit(BenchesChangedMsg{}), setStatus("Attached "+name+". Select it and press Enter to switch."))
		}
	default:
		var cmd tea.Cmd
		if f.focus == 0 {
			f.name, cmd = f.name.Update(msg)
		} else {
			f.path, cmd = f.path.Update(msg)
		}
		f.err = ""
		return m, cmd
	}
	if f.focus == 0 {
		f.path.Blur()
		f.name.Focus()
	} else {
		f.name.Blur()
		f.path.Focus()
	}
	return m, nil
}

// ── Drop ────────────────────────────────────────────────────────────────────

func (m BenchesModel) updateDrop(msg tea.KeyMsg) (BenchesModel, tea.Cmd) {
	name := m.dropping
	m.dropping = ""
	if msg.String() != "y" && msg.String() != "Y" {
		return m, nil
	}
	if err := m.mgr.DropBench(name); err != nil {
		return m, setError(err)
	}
	return m, tea.Batch(emit(BenchesChangedMsg{}), setStatus("Removed "+name+" from the registry. Its files were kept."))
}

// ── View ────────────────────────────────────────────────────────────────────

func (m BenchesModel) View(w, h int) string {
	out := lipgloss.JoinVertical(lipgloss.Left,
		panel(w, h-1, m.viewList(w-2)),
		helpBar(w, "↑↓", "select", "Enter", "switch", "n", "new bench", "a", "attach existing", "d", "drop", "r", "reload"),
	)
	switch {
	case m.form != nil:
		return overlay(out, m.viewForm(), w, h)
	case m.attach != nil:
		return overlay(out, m.viewAttach(), w, h)
	case m.dropping != "":
		return overlay(out, m.viewDrop(), w, h)
	}
	return out
}

func (m BenchesModel) viewList(w int) string {
	lines := []string{
		theme.StylePrimary.Render(fmt.Sprintf("Benches  (%d registered)", len(m.benches))),
		theme.StyleMuted.Render(strings.Repeat("─", max(w, 1))),
	}
	if m.loadErr != nil {
		return strings.Join(append(lines, theme.StyleLogError.Render(" "+m.loadErr.Error())), "\n")
	}
	lines = append(lines, theme.StyleMuted.Render(fmt.Sprintf("  %-22s %-10s %-16s %-6s %s", "NAME", "DB", "FRAPPE", "SITES", "STATUS")))
	if len(m.benches) == 0 {
		lines = append(lines, theme.StyleMuted.Render("  No benches registered. Press [n] to create one or [a] to attach one."))
	}
	start, end := m.nav.window(len(m.benches), m.listRows())
	for i := start; i < end; i++ {
		b := m.benches[i]
		status := ""
		switch {
		case b.Missing:
			status = "missing"
		case m.isCurrent[b.Name]:
			status = "★ active"
		case b.IsActive:
			status = "default"
		}
		row := fmt.Sprintf("  %-22s %-10s %-16s %-6d %s",
			pad(b.Name, 22), b.Entry.DBEngine, pad(core.FormatFrappeVersion(b.Entry.FrappeVersion), 16), len(b.Sites), status)
		switch {
		case i == m.nav.cursor:
			lines = append(lines, theme.StyleRowSelected.Render(row))
		case b.Missing:
			lines = append(lines, theme.StyleLogError.Render(row))
		default:
			lines = append(lines, row)
		}
	}
	if m.nav.cursor < len(m.benches) {
		lines = append(lines, theme.StyleMuted.Render("  "+m.benches[m.nav.cursor].Entry.Path)+scrollHint(start, end, len(m.benches)))
	}
	if !m.current.Registered {
		lines = append(lines, theme.StyleBadgeWarn.Render("  This session uses "+m.current.Path+", which is not registered: [a] attaches it."))
	} else if reason := m.current.PinReason(); reason != "" {
		lines = append(lines, theme.StyleBadgeWarn.Render("  This session's bench is "+reason+"; Enter switches it and the machine default."))
	}
	return strings.Join(lines, "\n")
}

func (m BenchesModel) viewForm() string {
	f := m.form
	rows := []string{
		formRow(f.focus == 0, "Name", f.name.View()),
		formRow(f.focus == 1, "Database", engineChoice(f.engine)),
	}
	if f.engine == "postgres" {
		rows = append(rows, formNote(dbLine(f.db, "postgres", false)))
	}
	rows = append(rows, formRow(f.focus == 2, "Frappe version", f.version.View()))
	for _, l := range m.planPreview(f.version.Value()) {
		rows = append(rows, formNote(l))
	}
	if f.err != "" {
		rows = append(rows, "", errorText(f.err))
	}
	keys := theme.StyleMuted.Render("[Tab/Enter] Next  [Shift+Tab] Back  [Esc] Cancel")
	if f.focus == 2 {
		keys = theme.StylePrimary.Render("[Enter] Create bench") + theme.StyleMuted.Render("  [Tab] Fields  [Esc] Cancel")
	}
	return dialog("Create New Bench", rows, keys)
}

func (m BenchesModel) viewAttach() string {
	f := m.attach
	rows := []string{
		formRow(f.focus == 0, "Name", f.name.View()),
		formRow(f.focus == 1, "Directory", f.path.View()),
		formNote(theme.StyleMuted.Render("Database and Frappe version are read from it.")),
	}
	if f.err != "" {
		rows = append(rows, "", errorText(f.err))
	}
	return dialog("Attach Existing Bench", rows, theme.StyleMuted.Render("[Tab] Fields   [Enter] Attach   [Esc] Cancel"))
}

func (m BenchesModel) viewDrop() string {
	return dialog("Remove bench '"+m.dropping+"' from the registry?",
		[]string{"Its directory and sites stay on disk; attach it again with [a]."},
		theme.StylePrimary.Render("[y] Remove")+"   "+theme.StyleMuted.Render("[any other key] Cancel"))
}
