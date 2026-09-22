package views

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

type catalogMsg struct{ cat core.Catalog }

type fpmVersionMsg struct {
	version string
	latest  string
}

type fpmUpdatedMsg struct {
	newVersion string
	err        error
}

type detailsMsg struct {
	key     string
	details core.PackageDetails
	err     error
}

type detailState struct {
	loading bool
	details core.PackageDetails
	err     error
}

// MarketplaceModel browses the fpm registry and installs apps.
type MarketplaceModel struct {
	fpm   *core.FPMClient
	bench core.ActiveBench

	catalog    core.Catalog
	loading    bool
	filtered   []core.FPMPackage
	nav        listNav
	category   string
	categories []string

	searching bool
	search    textinput.Model

	details   map[string]*detailState
	installed map[string]string
	sites     []string
	target    int // 0 installs into the bench only; n installs onto sites[n-1] too

	fpmVersion  string
	fpmLatest   string
	fpmUpdating bool
	confirming  bool

	inspector viewport.Model
	width     int
	height    int
}

// NewMarketplaceModel creates the marketplace for bench.
func NewMarketplaceModel(fpm *core.FPMClient, bench core.ActiveBench) MarketplaceModel {
	si := textinput.New()
	si.Placeholder = "search packages…"
	si.CharLimit = 60
	si.Width = 30

	vp := viewport.New(40, 20)
	// The list owns ↑↓ and PgUp/PgDn; the inspector scrolls with Ctrl+U/D
	// and the mouse wheel.
	vp.KeyMap = viewport.KeyMap{
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
	}

	return MarketplaceModel{
		fpm:        fpm,
		bench:      bench,
		loading:    true,
		category:   "all",
		categories: []string{"all"},
		search:     si,
		details:    map[string]*detailState{},
		installed:  core.InstalledApps(bench.Path),
		sites:      core.DiscoverSites(bench.Path),
		inspector:  vp,
	}
}

// Init fetches the catalog and checks FPM version in the background.
func (m MarketplaceModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCatalog(), m.checkFPMVersion())
}

func (m MarketplaceModel) fetchCatalog() tea.Cmd {
	fpm := m.fpm
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return catalogMsg{cat: fpm.FetchCatalog(ctx)}
	}
}

func (m MarketplaceModel) checkFPMVersion() tea.Cmd {
	return func() tea.Msg {
		bin, err := core.FindFPM()
		ver := ""
		if err == nil {
			ver = core.FPMVersion(bin)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cacheDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "vybench")
		if core.DetectPlatform() == core.PlatformSnap {
			if common := os.Getenv("SNAP_COMMON"); common != "" {
				cacheDir = common
			}
		}
		latest, _ := core.CheckLatestFPM(ctx, nil, cacheDir)
		return fpmVersionMsg{version: ver, latest: latest}
	}
}

func (m MarketplaceModel) updateFPM() tea.Cmd {
	tag := m.fpmLatest
	if tag == "" {
		tag = "latest"
	}
	destDir := core.DynamicFPMDirectory()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		installedPath, err := core.DownloadFPM(ctx, nil, tag, destDir)
		if err != nil {
			return fpmUpdatedMsg{err: err}
		}
		newVer := core.FPMVersion(installedPath)
		return fpmUpdatedMsg{newVersion: newVer}
	}
}

// CapturingInput reports whether the search box owns the keyboard.
func (m MarketplaceModel) CapturingInput() bool { return m.searching || m.confirming }

// SetSize sets the content area.
func (m *MarketplaceModel) SetSize(w, h int) {
	m.width, m.height = w, h
	_, inspW := m.split(w)
	m.inspector.Width = max(inspW-2, 10)
	m.inspector.Height = max(h-4, 3)
	m.nav.fix(len(m.filtered), m.listRows())
	m.refreshInspector()
}

func (m MarketplaceModel) split(w int) (int, int) {
	listW := w * 55 / 100
	return listW, w - listW - 1
}

func (m MarketplaceModel) listRows() int {
	rows := m.height - 7
	if m.searching || m.search.Value() != "" {
		rows--
	}
	return max(rows, 1)
}

func (m MarketplaceModel) selected() (core.FPMPackage, bool) {
	if m.nav.cursor < len(m.filtered) {
		return m.filtered[m.nav.cursor], true
	}
	return core.FPMPackage{}, false
}

func (m MarketplaceModel) targetSite() string {
	if m.target > 0 && m.target <= len(m.sites) {
		return m.sites[m.target-1]
	}
	return ""
}

// SetTargetSite preselects the given site as the installation target.
func (m *MarketplaceModel) SetTargetSite(site string) {
	for i, s := range m.sites {
		if s == site {
			m.target = i + 1
			m.refreshInspector()
			return
		}
	}
	m.sites = core.DiscoverSites(m.bench.Path)
	for i, s := range m.sites {
		if s == site {
			m.target = i + 1
			m.refreshInspector()
			return
		}
	}
	if site != "" {
		m.sites = append(m.sites, site)
		m.target = len(m.sites)
		m.refreshInspector()
	}
}

func (m MarketplaceModel) Update(msg tea.Msg) (MarketplaceModel, tea.Cmd) {
	switch msg := msg.(type) {
	case catalogMsg:
		m.loading = false
		m.catalog = msg.cat
		m.categories = core.Categories(msg.cat.Packages)
		m.details = map[string]*detailState{}
		m.applyFilter()
		cmds := []tea.Cmd{m.ensureDetails()}
		if msg.cat.Offline {
			cmds = append(cmds, emit(StatusMsg{
				Text: fmt.Sprintf("fpm registry unreachable (%v): showing the built-in app list", msg.cat.Err),
				Err:  true,
			}))
		}
		return m, tea.Batch(cmds...)

	case detailsMsg:
		m.details[msg.key] = &detailState{details: msg.details, err: msg.err}
		m.refreshInspector()
		return m, nil

	case fpmVersionMsg:
		m.fpmVersion = msg.version
		m.fpmLatest = msg.latest
		return m, nil

	case fpmUpdatedMsg:
		m.fpmUpdating = false
		if msg.err != nil {
			return m, emit(StatusMsg{Text: "FPM update failed: " + msg.err.Error(), Err: true})
		}
		if msg.newVersion != "" {
			m.fpmVersion = msg.newVersion
		}
		return m, emit(StatusMsg{Text: fmt.Sprintf("FPM updated to %s successfully.", m.fpmVersion)})

	case BenchSwitchedMsg:
		m.bench = msg.Bench
		m.installed = core.InstalledApps(m.bench.Path)
		m.sites = core.DiscoverSites(m.bench.Path)
		m.target = 0
		m.refreshInspector()
		return m, nil

	case SitesChangedMsg:
		m.sites = core.DiscoverSites(m.bench.Path)
		m.target = min(m.target, len(m.sites))
		m.refreshInspector()
		return m, nil

	case AppsChangedMsg:
		m.installed = core.InstalledApps(m.bench.Path)
		m.refreshInspector()
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.inspector, cmd = m.inspector.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if m.confirming {
			m.confirming = false
			if msg.String() == "y" || msg.String() == "Y" {
				return m, m.install()
			}
			return m, nil
		}
		if m.searching {
			switch msg.String() {
			case "esc":
				m.search.SetValue("")
				fallthrough
			case "enter":
				m.searching = false
				m.search.Blur()
				m.applyFilter()
				cmd := m.ensureDetails()
				return m, cmd
			}
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			m.applyFilter()
			cmd = tea.Batch(cmd, m.ensureDetails())
			return m, cmd
		}

		if m.nav.key(msg.String(), len(m.filtered), m.listRows()) {
			m.refreshInspector()
			cmd := m.ensureDetails()
			return m, cmd
		}
		switch msg.String() {
		case "/":
			m.searching = true
			cmd := m.search.Focus()
			return m, cmd
		case "left", "h", "right", "l":
			delta := 1
			if k := msg.String(); k == "left" || k == "h" {
				delta = -1
			}
			m.shiftCategory(delta)
			cmd := m.ensureDetails()
			return m, cmd
		case "t":
			m.target = (m.target + 1) % (len(m.sites) + 1)
			m.refreshInspector()
		case "r":
			m.loading = true
			return m, m.fetchCatalog()
		case "i":
			if pkg, ok := m.selected(); ok {
				if d := m.details[pkg.FullName()]; d != nil && d.details.WheelPlatform != "" && !core.WheelsMatchHost(d.details.WheelPlatform) {
					return m, setError(fmt.Errorf("these wheels were built for %s, which is not this machine", d.details.WheelPlatform))
				}
			}
			m.confirming = true
			return m, nil
		case "u":
			if m.fpmUpdating {
				return m, nil
			}
			m.fpmUpdating = true
			return m, tea.Batch(
				emit(StatusMsg{Text: "Updating FPM..."}),
				m.updateFPM(),
			)
		default:
			var cmd tea.Cmd
			m.inspector, cmd = m.inspector.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m MarketplaceModel) install() tea.Cmd {
	pkg, ok := m.selected()
	if !ok {
		return nil
	}
	site := m.targetSite()
	spec, err := m.fpm.InstallSpec(pkg, m.bench.Path, site)
	if err != nil {
		return setError(err)
	}
	where := "the bench"
	if site != "" {
		where = site
	}
	return runJob("Install "+pkg.VersionedName(), spec, installedMessage(pkg.FullName(), where, site != ""),
		AppsChangedMsg{})
}

// ensureDetails starts loading the selected package's metadata once.
func (m *MarketplaceModel) ensureDetails() tea.Cmd {
	pkg, ok := m.selected()
	if !ok || m.catalog.Offline {
		return nil
	}
	k := pkg.FullName()
	if _, seen := m.details[k]; seen {
		return nil
	}
	m.details[k] = &detailState{loading: true}
	m.refreshInspector()
	fpm := m.fpm
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d, err := fpm.FetchDetails(ctx, pkg)
		return detailsMsg{key: k, details: d, err: err}
	}
}

func (m *MarketplaceModel) applyFilter() {
	m.filtered = core.Filter(m.catalog.Packages, m.search.Value(), m.category)
	m.nav.fix(len(m.filtered), m.listRows())
	m.refreshInspector()
}

func (m *MarketplaceModel) shiftCategory(delta int) {
	n := len(m.categories)
	for i, c := range m.categories {
		if strings.EqualFold(c, m.category) {
			m.category = m.categories[(i+delta+n)%n]
			break
		}
	}
	m.nav = listNav{}
	m.applyFilter()
}

func (m *MarketplaceModel) refreshInspector() {
	m.inspector.SetContent(m.inspectorContent(max(m.inspector.Width, 20)))
	m.inspector.GotoTop()
}

func (m MarketplaceModel) inspectorContent(w int) string {
	pkg, ok := m.selected()
	if !ok {
		if m.loading {
			return theme.StyleMuted.Render("Loading the catalog from " + m.fpm.RegistryURL + "…")
		}
		return theme.StyleMuted.Render("No packages match the filter.")
	}
	var sb strings.Builder
	wrap := func(s string, width int) []string {
		return strings.Split(lipgloss.NewStyle().Width(max(width, 10)).Render(s), "\n")
	}
	// line prints a key and a value wrapped under a hanging indent.
	line := func(k, v string) {
		for i, part := range wrap(v, w-11) {
			if i == 0 {
				sb.WriteString(fmt.Sprintf("%-10s %s\n", k, part))
			} else {
				sb.WriteString(strings.Repeat(" ", 11) + part + "\n")
			}
		}
	}
	warn := func(s string) {
		for _, part := range wrap(s, w) {
			sb.WriteString(theme.StyleBadgeWarn.Render(part) + "\n")
		}
	}

	title := pkg.Title
	if title == "" {
		title = pkg.Name
	}
	sb.WriteString(theme.StyleBold.Render("📦 "+title) + "\n")
	sb.WriteString(theme.StyleMuted.Render(pkg.FullName()+"  ·  "+pkg.DisplayVersion()+"  ·  "+pkg.Category) + "\n")
	sb.WriteString(theme.StyleMuted.Render(strings.Repeat("─", max(w-1, 1))) + "\n")
	if pkg.Description != "" {
		sb.WriteString(strings.Join(wrap(pkg.Description, w), "\n") + "\n")
	}
	sb.WriteString("\n")

	if v, ok := m.installed[pkg.Name]; ok {
		status := theme.StyleLogOK.Render("✓ installed")
		if v != "" {
			status += " " + v
		}
		if v != "" && pkg.Version != "" && core.CompareVersions(pkg.Version, v) > 0 {
			status += theme.StyleBadgeWarn.Render("  → update available: " + pkg.Version)
		}
		line("Bench", status)
	} else {
		line("Bench", theme.StyleMuted.Render("not installed on '"+m.bench.Name+"'"))
	}

	st := m.details[pkg.FullName()]
	switch {
	case m.catalog.Offline:
		sb.WriteString("\n")
		warn("Offline: package details need the registry. [r] retries.")
	case st == nil || st.loading:
		sb.WriteString("\n" + theme.StyleMuted.Render("Loading details…") + "\n")
	case st.err != nil:
		for _, part := range wrap("Details unavailable: "+st.err.Error(), w) {
			sb.WriteString(theme.StyleLogError.Render(part) + "\n")
		}
	default:
		d := st.details
		if d.License != "" {
			line("License", d.License)
		}
		if d.Author != "" {
			line("Author", d.Author)
		}
		if len(d.ReleaseDate) >= 10 {
			line("Released", d.ReleaseDate[:10])
		}
		if d.SourceURL != "" {
			line("Source", d.SourceURL)
		}
		if len(d.FrappeCompat) > 0 {
			line("Frappe", strings.Join(d.FrappeCompat, ", "))
			if !compatible(d.FrappeCompat, m.bench.Entry.FrappeVersion) {
				warn("⚠ Built for Frappe " + strings.Join(d.FrappeCompat, ", ") + "; this bench runs " +
					core.FormatFrappeVersion(m.bench.Entry.FrappeVersion) + ".")
			}
		}
		if d.WheelPlatform != "" {
			v := d.WheelPlatform
			if d.WheelPython != "" {
				v += ", Python " + d.WheelPython
			}
			line("Wheels", v)
			if !core.WheelsMatchHost(d.WheelPlatform) {
				warn(fmt.Sprintf("⚠ These wheels were built for another platform; fpm refuses to install them on %s/%s.",
					runtime.GOOS, runtime.GOARCH))
			}
		}
		if len(d.RequiredApps) > 0 {
			line("Requires", strings.Join(d.RequiredApps, ", "))
		}
		if len(d.Dependencies) > 0 {
			line("Depends", strings.Join(d.Dependencies, ", "))
		}
		if len(d.Versions) > 1 {
			vs := d.Versions
			if len(vs) > 6 {
				vs = append(vs[:6:6], "…")
			}
			line("Versions", strings.Join(vs, ", "))
		}
	}

	sb.WriteString("\n")
	target := "the bench only"
	if s := m.targetSite(); s != "" {
		target = "the bench and site " + s
	}
	sb.WriteString(theme.StyleMuted.Render("Install into ") + target)
	if len(m.sites) > 0 {
		sb.WriteString(theme.StyleMuted.Render("  [t] change"))
	}
	sb.WriteString("\n" + theme.StylePrimary.Render("[i] Install "+pkg.VersionedName()))
	return sb.String()
}

// compatible reports whether benchVersion's major is in a package's
// frappe_compatibility list (entries such as "16" or "version-16").
func compatible(compat []string, benchVersion string) bool {
	major := core.MajorVersion(benchVersion)
	if major == "" {
		return true
	}
	for _, c := range compat {
		if core.MajorVersion(strings.TrimPrefix(c, "version-")) == major {
			return true
		}
	}
	return false
}

func (m MarketplaceModel) View(w, h int) string {
	listW, inspW := m.split(w)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		panel(listW, h-1, m.viewList(listW-2)),
		" ",
		panel(inspW, h-1, theme.StyleBold.Render("App Inspector")+"\n"+m.inspector.View()),
	)
	help := helpBar(w, "↑↓", "select", "←→", "category", "/", "search", "t", "target site", "i", "install", "r", "reload", "^U/^D", "scroll details")
	out := lipgloss.JoinVertical(lipgloss.Left, body, help)
	if m.confirming {
		name := "this app"
		if pkg, ok := m.selected(); ok {
			name = pkg.VersionedName()
		}
		where := "the bench"
		if site := m.targetSite(); site != "" {
			where = site
		}
		out = overlay(out, dialog("Install "+name+" into "+where+"?",
			[]string{"fpm writes the app into this bench. This cannot be undone from here."},
			theme.StylePrimary.Render("[y] Install")+"   "+theme.StyleMuted.Render("[any other key] Cancel")), w, h)
	}
	return out
}

func (m MarketplaceModel) viewList(w int) string {
	lines := []string{pillBar(m.categories, m.category, w)}
	if m.searching || m.search.Value() != "" {
		lines = append(lines, " Search: "+m.search.View())
	}
	lines = append(lines, theme.StyleMuted.Render(strings.Repeat("─", max(w, 1))))

	if m.loading && len(m.catalog.Packages) == 0 {
		return strings.Join(append(lines, theme.StyleMuted.Render(" Loading "+m.fpm.RegistryURL+"…")), "\n")
	}
	if len(m.filtered) == 0 {
		return strings.Join(append(lines, theme.StyleMuted.Render(" No packages found.")), "\n")
	}

	verW, catW := 14, 13
	nameW := max(w-2-verW-catW-3, 12)
	rows := m.listRows()
	start, end := m.nav.window(len(m.filtered), rows)
	for i := start; i < end; i++ {
		p := m.filtered[i]
		mark := "  "
		if _, ok := m.installed[p.Name]; ok {
			mark = "✓ "
		}
		row := mark + pad(p.FullName(), nameW) + " " + pad(p.DisplayVersion(), verW) + " " + pad(p.Category, catW)
		switch {
		case i == m.nav.cursor:
			lines = append(lines, theme.StyleRowSelected.Render(row))
		case i%2 == 0:
			lines = append(lines, theme.StyleRowAlt.Render(row))
		default:
			lines = append(lines, theme.StyleRowNormal.Render(row))
		}
	}

	footer := theme.StyleMuted.Render(fmt.Sprintf(" %d packages from %s", len(m.catalog.Packages), m.catalog.Source))
	if m.catalog.Offline {
		footer = theme.StyleBadgeWarn.Render(" ⚠ offline catalog (registry unreachable)")
	}
	if m.fpmVersion != "" {
		fpmText := "  ·  fpm " + m.fpmVersion
		if m.fpmLatest != "" && m.fpmLatest != "v"+m.fpmVersion && m.fpmLatest != m.fpmVersion {
			fpmText += " (" + m.fpmLatest + " available, [u] update)"
		}
		footer += theme.StyleMuted.Render(fpmText)
	}
	return strings.Join(append(lines, footer+scrollHint(start, end, len(m.filtered))), "\n")
}

// pad truncates or pads s to exactly w cells.
func pad(s string, w int) string {
	if lipgloss.Width(s) > w {
		return fit(s, w, 1)
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// pillBar renders the category pills that fit in w, always including the
// active one, with ‹ › marking hidden pills.
func pillBar(cats []string, active string, w int) string {
	pills := make([]string, len(cats))
	widths := make([]int, len(cats))
	at := 0
	for i, c := range cats {
		if strings.EqualFold(c, active) {
			pills[i], at = theme.StyleTabActive.Render(c), i
		} else {
			pills[i] = theme.StyleTabInactive.Render(c)
		}
		widths[i] = lipgloss.Width(pills[i])
	}
	if len(cats) == 0 {
		return ""
	}
	lo, hi, total := at, at, widths[at]
	for grew := true; grew; {
		grew = false
		if hi+1 < len(cats) && total+widths[hi+1] <= w-2 {
			hi++
			total += widths[hi]
			grew = true
		}
		if lo > 0 && total+widths[lo-1] <= w-2 {
			lo--
			total += widths[lo]
			grew = true
		}
	}
	left, right := " ", " "
	if lo > 0 {
		left = theme.StyleMuted.Render("‹")
	}
	if hi < len(cats)-1 {
		right = theme.StyleMuted.Render("›")
	}
	return left + lipgloss.JoinHorizontal(lipgloss.Top, pills[lo:hi+1]...) + right
}

// installedMessage is the status shown once an app is installed. The running
// web process loaded its apps when it started, so a site that now has an app
// the process has not loaded returns HTTP 500 ("No module named ...") until
// services restart -- measured on a real install. That consequence goes first,
// because the status line is one row and gets truncated at the right.
func installedMessage(app, where string, onSite bool) string {
	if onSite {
		return fmt.Sprintf("Installed %s on %s. Restart services NOW ([r] on Overview): until then %s returns HTTP 500.", app, where, where)
	}
	return fmt.Sprintf("Installed %s into %s. Restart services ([r] on Overview) so they load it.", app, where)
}
