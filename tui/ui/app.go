// Package ui — the root Bubble Tea model of the vybench TUI: tabs, the active
// bench, the status bar, the job overlay, and message routing.
package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
	"github.com/vyogotech/vybench/tui/ui/views"
)

// Tabs, in order.
const (
	TabOverview = iota
	TabSites
	TabMarketplace
	TabLogs
	TabBenches
	tabCount
)

var tabNames = []string{"[1] Overview", "[2] Sites", "[3] Marketplace", "[4] Logs", "[5] Benches"}

const (
	minWidth    = 60
	minHeight   = 16
	statusLasts = 12 * time.Second
)

type clearStatusMsg struct{ seq int }

// App is the root Bubble Tea model.
type App struct {
	tab    int
	width  int
	height int
	bench  core.ActiveBench

	overview views.OverviewModel
	sites    views.SitesModel
	market   views.MarketplaceModel
	logs     views.LogsModel
	benches  views.BenchesModel
	job      views.JobModel

	status    string
	statusErr bool
	statusSeq int
}

// NewApp creates the root model for the active bench.
func NewApp(mgr *core.Manager, sup *core.Supervisor, fpm *core.FPMClient) App {
	bench := mgr.ResolveActive()
	a := App{
		bench:    bench,
		overview: views.NewOverviewModel(sup, mgr, bench),
		sites:    views.NewSitesModel(mgr, sup, bench),
		market:   views.NewMarketplaceModel(fpm, bench),
		logs:     views.NewLogsModel(bench),
		benches:  views.NewBenchesModel(mgr, bench),
	}
	_ = a.overview.SetVisible(true) // Init performs the first refresh
	return a
}

func (a App) Init() tea.Cmd {
	return tea.Batch(a.overview.Init(), a.market.Init())
}

// Shutdown stops a job that is still running when the program exits.
func (a App) Shutdown() { a.job.Cancel() }

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.resize()
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)

	case tea.MouseMsg:
		if a.job.Active() {
			return a, nil
		}
		return a.routeActive(msg)

	case views.RunJobMsg:
		if a.job.Running() {
			return a, statusCmd("Another job is still running; wait for it or cancel it with [x]", true)
		}
		var cmd tea.Cmd
		a.job, cmd = views.NewJob(msg)
		a.job.SetSize(a.width, a.contentHeight())
		return a, cmd

	case views.StatusMsg:
		a.status, a.statusErr = msg.Text, msg.Err
		a.statusSeq++
		seq := a.statusSeq
		return a, tea.Tick(statusLasts, func(time.Time) tea.Msg { return clearStatusMsg{seq: seq} })

	case clearStatusMsg:
		if msg.seq == a.statusSeq {
			a.status = ""
		}
		return a, nil

	case views.BenchSwitchedMsg:
		a.bench = msg.Bench
		// Keep this session, and every command it starts, on the chosen bench
		// even when it was launched pinned to another one.
		_ = os.Setenv("VYBENCH_BENCH", msg.Bench.Path)
	}
	return a.broadcast(msg)
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" && !a.job.Running() {
		return a, tea.Quit
	}
	if a.job.Active() {
		var cmd tea.Cmd
		a.job, cmd = a.job.Update(msg)
		return a, cmd
	}
	if !a.capturing() {
		switch k := msg.String(); k {
		case "q":
			return a, tea.Quit
		case "1", "2", "3", "4", "5":
			return a.setTab(int(k[0] - '1'))
		case "tab":
			return a.setTab((a.tab + 1) % tabCount)
		case "shift+tab":
			return a.setTab((a.tab + tabCount - 1) % tabCount)
		case "b":
			return a.setTab(TabBenches)
		}
	}
	return a.routeActive(msg)
}

// broadcast delivers msg to every view: results of background work must
// reach their view whichever tab is showing.
func (a App) broadcast(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmds := make([]tea.Cmd, 0, 6)
	var cmd tea.Cmd
	a.overview, cmd = a.overview.Update(msg)
	cmds = append(cmds, cmd)
	a.sites, cmd = a.sites.Update(msg)
	cmds = append(cmds, cmd)
	a.market, cmd = a.market.Update(msg)
	cmds = append(cmds, cmd)
	a.logs, cmd = a.logs.Update(msg)
	cmds = append(cmds, cmd)
	a.benches, cmd = a.benches.Update(msg)
	cmds = append(cmds, cmd)
	if a.job.Active() {
		a.job, cmd = a.job.Update(msg)
		cmds = append(cmds, cmd)
	}
	return a, tea.Batch(cmds...)
}

// routeActive delivers input to the visible tab only.
func (a App) routeActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.tab {
	case TabOverview:
		a.overview, cmd = a.overview.Update(msg)
	case TabSites:
		a.sites, cmd = a.sites.Update(msg)
	case TabMarketplace:
		a.market, cmd = a.market.Update(msg)
	case TabLogs:
		a.logs, cmd = a.logs.Update(msg)
	case TabBenches:
		a.benches, cmd = a.benches.Update(msg)
	}
	return a, cmd
}

func (a App) setTab(t int) (tea.Model, tea.Cmd) {
	a.tab = t
	c1 := a.overview.SetVisible(t == TabOverview)
	c2 := a.logs.SetVisible(t == TabLogs)
	c3 := a.sites.SetVisible(t == TabSites)
	return a, tea.Batch(c1, c2, c3)
}

func (a App) capturing() bool {
	switch a.tab {
	case TabSites:
		return a.sites.CapturingInput()
	case TabMarketplace:
		return a.market.CapturingInput()
	case TabLogs:
		return a.logs.CapturingInput()
	case TabBenches:
		return a.benches.CapturingInput()
	}
	return false
}

func (a App) contentHeight() int { return max(a.height-3, 5) } // header, tabs, status bar

func (a *App) resize() {
	w, h := a.width, a.contentHeight()
	a.overview.SetSize(w, h)
	a.sites.SetSize(w, h)
	a.market.SetSize(w, h)
	a.logs.SetSize(w, h)
	a.benches.SetSize(w, h)
	a.job.SetSize(w, h)
}

func statusCmd(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return views.StatusMsg{Text: text, Err: isErr} }
}

// ── View ────────────────────────────────────────────────────────────────────

func (a App) View() string {
	if a.width == 0 {
		return "Loading…"
	}
	if a.width < minWidth || a.height < minHeight {
		return fmt.Sprintf("The terminal is %d×%d; vybench needs at least %d×%d.", a.width, a.height, minWidth, minHeight)
	}
	h := a.contentHeight()
	content := views.Fit(a.renderContent(h), a.width, h)
	if a.job.Active() {
		content = views.Overlay(content, a.job.View(), a.width, h)
	}
	return views.Fit(strings.Join([]string{a.renderHeader(), a.renderTabs(), content, a.renderStatusBar()}, "\n"), a.width, a.height)
}

func (a App) renderHeader() string {
	b := a.bench
	title := theme.StyleHeaderTitle.Render("VYBENCH")
	bench := theme.StylePrimary.Render(fmt.Sprintf("Bench: %s (%s • %s)",
		b.Name, core.FormatFrappeVersion(b.Entry.FrappeVersion), b.Entry.DBEngine))
	if !b.Registered {
		bench += theme.StyleBadgeWarn.Render("  unregistered")
	}
	if r := b.PinReason(); r != "" {
		bench += theme.StyleBadgeWarn.Render("  ⚑ " + r)
	}
	right := theme.StyleMuted.Render("[b] benches")
	inner := a.width - 2
	gap := inner - lipgloss.Width(title) - lipgloss.Width(bench) - lipgloss.Width(right) - 4
	line := title + "    " + bench
	if gap > 0 {
		line += strings.Repeat(" ", gap) + right
	}
	return theme.StyleHeader.Width(a.width).Render(views.Fit(line, inner, 1))
}

func (a App) renderTabs() string {
	tabs := make([]string, len(tabNames))
	for i, name := range tabNames {
		if i == a.tab {
			tabs[i] = theme.StyleTabActive.Render(name)
		} else {
			tabs[i] = theme.StyleTabInactive.Render(name)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (a App) renderContent(h int) string {
	switch a.tab {
	case TabOverview:
		return a.overview.View(a.width, h)
	case TabSites:
		return a.sites.View(a.width, h)
	case TabMarketplace:
		return a.market.View(a.width, h)
	case TabLogs:
		return a.logs.View(a.width, h)
	case TabBenches:
		return a.benches.View(a.width, h)
	}
	return ""
}

func (a App) renderStatusBar() string {
	keys := theme.StyleHelpKey.Render("[1-5]") + " tabs  " +
		theme.StyleHelpKey.Render("[Tab]") + " next  " +
		theme.StyleHelpKey.Render("[b]") + " benches  " +
		theme.StyleHelpKey.Render("[q]") + " quit"
	inner := a.width - 2
	line := keys
	if a.status != "" {
		style := theme.StyleLogOK
		if a.statusErr {
			style = theme.StyleLogError
		}
		// The key legend yields to a status that needs the room.
		room := inner - lipgloss.Width(keys) - 2
		if lipgloss.Width(a.status) <= room {
			line = style.Render(a.status) + strings.Repeat(" ", room-lipgloss.Width(a.status)+2) + keys
		} else {
			line = style.Render(views.Fit(a.status, inner, 1))
		}
	}
	return theme.StyleHelp.Width(a.width).Render(views.Fit(line, inner, 1))
}
