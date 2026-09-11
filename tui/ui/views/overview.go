package views

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

const serviceRefreshEvery = 5 * time.Second

type servicesMsg struct {
	services []core.Service
	err      error
	health   core.BenchHealth
	serving  string // name of the bench the web process serves
}

type overviewTickMsg struct{}

// OverviewModel shows service status and the active bench.
type OverviewModel struct {
	sup      *core.Supervisor
	mgr      *core.Manager
	bench    core.ActiveBench
	health   *core.BenchHealth
	serving  string
	sites    []string
	services []core.Service
	svcErr   error
	loading  bool
	visible  bool
	width    int
	height   int
}

// NewOverviewModel creates the overview for bench.
func NewOverviewModel(sup *core.Supervisor, mgr *core.Manager, bench core.ActiveBench) OverviewModel {
	return OverviewModel{sup: sup, mgr: mgr, bench: bench, sites: core.DiscoverSites(bench.Path), loading: true}
}

// Init loads service status and starts the refresh timer.
func (m OverviewModel) Init() tea.Cmd {
	return tea.Batch(m.refresh(), overviewTick())
}

func (m OverviewModel) refresh() tea.Cmd {
	sup, mgr, bench := m.sup, m.mgr, m.bench
	return func() tea.Msg {
		msg := servicesMsg{}
		msg.services, msg.err = sup.Status()
		var candidates []string
		if mgr != nil {
			candidates = mgr.BenchPaths()
		}
		msg.health = core.CheckBench(bench.Path, engineOr(bench.Entry.DBEngine), candidates)
		if msg.health.Serving != "" && mgr != nil {
			msg.serving = mgr.NameFor(msg.health.Serving)
		}
		return msg
	}
}

func overviewTick() tea.Cmd {
	return tea.Tick(serviceRefreshEvery, func(time.Time) tea.Msg { return overviewTickMsg{} })
}

// SetSize sets the content area.
func (m *OverviewModel) SetSize(w, h int) { m.width, m.height = w, h }

// SetVisible is called on tab changes; status is only polled while visible.
func (m *OverviewModel) SetVisible(v bool) tea.Cmd {
	was := m.visible
	m.visible = v
	if v && !was && !m.loading {
		m.loading = true
		return m.refresh()
	}
	return nil
}

func (m OverviewModel) Update(msg tea.Msg) (OverviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case servicesMsg:
		m.services, m.svcErr, m.loading = msg.services, msg.err, false
		h := msg.health
		m.health, m.serving = &h, msg.serving

	case overviewTickMsg:
		cmds := []tea.Cmd{overviewTick()}
		if m.visible && !m.loading {
			m.loading = true
			cmds = append(cmds, m.refresh())
		}
		return m, tea.Batch(cmds...)

	case ServicesChangedMsg:
		m.loading = true
		return m, m.refresh()

	case BenchSwitchedMsg:
		m.bench = msg.Bench
		m.sites = core.DiscoverSites(m.bench.Path)

	case SitesChangedMsg:
		m.sites = core.DiscoverSites(m.bench.Path)

	case tea.KeyMsg:
		action, title := "", ""
		switch msg.String() {
		case "s":
			action, title = "start", "Start services"
		case "x":
			action, title = "stop", "Stop services"
		case "r":
			action, title = "restart", "Restart services"
		case "u":
			m.loading = true
			return m, m.refresh()
		}
		if action != "" {
			steps, err := m.sup.ControlSteps(action)
			if err != nil {
				return m, setError(err)
			}
			return m, runJob(title, core.JobSpec{Steps: steps}, title+": done", ServicesChangedMsg{})
		}
	}
	return m, nil
}

func (m OverviewModel) View(w, h int) string {
	leftW := w / 2
	rightW := w - leftW - 1
	panelH := h - 1
	row := lipgloss.JoinHorizontal(lipgloss.Top,
		panel(leftW, panelH, m.renderServices(leftW-2)),
		" ",
		panel(rightW, panelH, m.renderBench(rightW-2, panelH-2)),
	)
	return lipgloss.JoinVertical(lipgloss.Left, row,
		helpBar(w, "s", "start all", "x", "stop all", "r", "restart all", "u", "refresh"))
}

func (m OverviewModel) renderServices(w int) string {
	title := theme.StylePrimary.Render("Services")
	if m.loading {
		title += theme.StyleMuted.Render("  refreshing…")
	}
	rows := []string{title, theme.StyleMuted.Render(strings.Repeat("─", max(w, 1)))}

	if m.svcErr != nil {
		rows = append(rows, theme.StyleLogError.Render(" "+m.svcErr.Error()))
	}
	if len(m.services) == 0 && m.svcErr == nil && !m.loading {
		rows = append(rows, theme.StyleMuted.Render(" No services found."))
	}
	for _, svc := range m.services {
		var badge string
		switch svc.State {
		case core.StateRunning:
			badge = theme.StyleBadgeRunning.Render("●")
		case core.StateStopped:
			badge = theme.StyleBadgeStopped.Render("○")
		case core.StateFailed:
			badge = theme.StyleLogError.Render("✖")
		case core.StateConflict:
			badge = theme.StyleBadgeWarn.Render("⚠")
		default:
			badge = theme.StyleBadgeWarn.Render("?")
		}
		line := fmt.Sprintf(" %s  %s", badge, theme.StyleBold.Render(svc.Label))
		detail := ""
		if svc.Detail != "" {
			detail = theme.StyleMuted.Render("  (" + svc.Detail + ")")
		}
		if lipgloss.Width(line+detail) <= w {
			rows = append(rows, line+detail)
		} else {
			rows = append(rows, line, "    "+theme.StyleMuted.Render(svc.Detail))
		}
	}
	return strings.Join(rows, "\n")
}

// healthLines say whether this bench's database answers and whether its
// web port is served, which service states alone cannot tell.
func (m OverviewModel) healthLines(w int) []string {
	h := m.health
	if h == nil {
		return []string{" " + theme.StyleMuted.Render("Checking the database and web server…")}
	}
	engine := engineOr(m.bench.Entry.DBEngine)
	db := theme.StyleLogOK.Render("✔ " + core.DBLabel(engine) + " answers")
	if !h.DBUp {
		db = theme.StyleLogError.Render("✖ " + core.DBLabel(engine) + " is not reachable")
	}
	// note wraps an explanation under its line instead of cutting it off.
	note := func(text string) []string {
		var out []string
		for _, l := range strings.Split(lipgloss.NewStyle().Width(max(w-11, 10)).Render(text), "\n") {
			out = append(out, strings.Repeat(" ", 10)+theme.StyleBadgeWarn.Render(strings.TrimRight(l, " ")))
		}
		return out
	}
	lines := []string{"", " Database " + db}
	if !h.DBUp {
		why := "press [s] to start it"
		if h.DBWhy != "" {
			why = h.DBWhy + "; [s] fixes it"
		}
		lines = append(lines, note(why)...)
	}
	web := theme.StyleLogOK.Render(fmt.Sprintf("✔ http://127.0.0.1:%d", h.WebPort))
	if !h.WebUp {
		web = theme.StyleLogError.Render(fmt.Sprintf("✖ nothing on port %d", h.WebPort))
	}
	lines = append(lines, " Web      "+web)
	if h.WebUp && h.Serving != "" && !core.SamePath(h.Serving, m.bench.Path) {
		lines = append(lines, note("serving bench '"+m.serving+"'; [r] restarts it on this one")...)
	}
	return lines
}

func (m OverviewModel) renderBench(w, h int) string {
	b := m.bench
	lines := []string{
		theme.StylePrimary.Render("Active Bench"),
		theme.StyleMuted.Render(strings.Repeat("─", max(w, 1))),
		fmt.Sprintf(" Name     %s", theme.StyleBold.Render(b.Name)),
		fmt.Sprintf(" Frappe   %s", core.FormatFrappeVersion(b.Entry.FrappeVersion)),
		fmt.Sprintf(" DB       %s", theme.DBBadge(b.Entry.DBEngine)),
		fmt.Sprintf(" Path     %s", theme.StyleMuted.Render(truncateLeft(b.Path, max(w-10, 10)))),
	}
	if reason := b.PinReason(); reason != "" {
		lines = append(lines, " "+theme.StyleBadgeWarn.Render("⚑ "+reason))
	}
	lines = append(lines, m.healthLines(w)...)
	if !b.Registered {
		lines = append(lines, " "+theme.StyleBadgeWarn.Render("⚑ not registered — attach it in [5] Benches"))
	}
	lines = append(lines, "", theme.StyleMuted.Render(fmt.Sprintf(" Sites (%d)", len(m.sites))))
	if len(m.sites) == 0 {
		lines = append(lines, theme.StyleMuted.Render("   (none yet — create one in [2] Sites)"))
	}
	room := h - len(lines)
	for i, s := range m.sites {
		if i == room-1 && len(m.sites) > room {
			lines = append(lines, theme.StyleMuted.Render(fmt.Sprintf("   … and %d more", len(m.sites)-i)))
			break
		}
		lines = append(lines, "   "+theme.StyleAccent.Render("◆ ")+s)
	}
	return strings.Join(lines, "\n")
}
