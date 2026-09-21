package views

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/theme"
)

// siteCheckMsg carries the verdict on an existing site.
type siteCheckMsg struct {
	bench, site string
	check       core.SiteCheck
}

// siteFailedMsg marks a site whose creation just failed.
type siteFailedMsg struct{ bench, site string }

// siteScanMsg is one step of the background check of every site: its
// verdict, and the sites still to check.
type siteScanMsg struct {
	bench, site string
	check       core.SiteCheck
	rest        []string
}

func scanSitesCmd(bench string, sites []string) tea.Cmd {
	if len(sites) == 0 {
		return nil
	}
	return func() tea.Msg {
		return siteScanMsg{bench: bench, site: sites[0], check: core.CheckSite(bench, sites[0]), rest: sites[1:]}
	}
}

func checkSiteCmd(bench, site string) tea.Cmd {
	return func() tea.Msg { return siteCheckMsg{bench: bench, site: site, check: core.CheckSite(bench, site)} }
}

func newInput(placeholder string, limit int, secret bool) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = limit
	in.Width = 32
	if secret {
		in.EchoMode = textinput.EchoPassword
	}
	return in
}

// toggleKey reports whether a key flips a checkbox or cycles a choice, and
// in which direction.
func toggleKey(k string) (int, bool) {
	switch k {
	case "left", "h":
		return -1, true
	case "right", "l", " ", "x":
		return 1, true
	}
	return 0, false
}

// ── New site form ───────────────────────────────────────────────────────────

// Fields of the new-site form, in tab order. PostgreSQL adds the superuser
// fields frappe cannot find itself; Force appears once the name is taken.
const (
	fieldBench = iota
	fieldDomain
	fieldAdminPW
	fieldEngine
	fieldApps
	fieldRootUser
	fieldRootPW
	fieldForce
)

// siteBench is a bench the new-site form can create a site on.
type siteBench struct {
	name, path, engine string
	active             bool
}

type siteForm struct {
	benches                           []siteBench
	bench                             int // index into benches
	domain, adminPW, rootUser, rootPW textinput.Model
	engine                            string
	focus                             int
	err                               string
	db                                dbState // the target bench's database server
	dbWhy                             string
	exists                            bool            // the name is taken on the target bench
	checked                           string          // "<bench>|<site>" that check is about
	check                             *core.SiteCheck // nil while checking
	force                             bool
	apps                              []string // bench apps that can be installed with the site
	appOn                             map[string]bool
	appAt                             int
}

// newSiteForm opens the form on benches[0], the active bench. With more than
// one bench to choose from, the bench field is focused first.
func newSiteForm(benches []siteBench) (*siteForm, tea.Cmd) {
	f := &siteForm{
		benches:  benches,
		domain:   newInput("mysite.localhost", 60, false),
		adminPW:  newInput("admin password", 64, true),
		rootUser: newInput("postgres", 40, false),
		rootPW:   newInput("blank for trust auth", 64, true),
		engine:   benches[0].engine,
	}
	f.loadApps()
	if len(benches) > 1 {
		f.setFocus(fieldBench)
	} else {
		f.setFocus(fieldDomain)
	}
	return f, f.checkDB()
}

// loadApps lists the target bench's apps; ERPNext starts ticked, since it is
// what most sites are for.
func (f *siteForm) loadApps() {
	f.apps = core.BenchAppNames(f.target().path)
	f.appOn = map[string]bool{"erpnext": slices.Contains(f.apps, "erpnext")}
	f.appAt = 0
}

func (f *siteForm) chosenApps() []string {
	var out []string
	for _, a := range f.apps {
		if f.appOn[a] {
			out = append(out, a)
		}
	}
	return out
}

func (f *siteForm) target() siteBench { return f.benches[f.bench] }
func (f *siteForm) name() string      { return strings.TrimSpace(f.domain.Value()) }

func (f *siteForm) checkDB() tea.Cmd {
	f.db = dbUnknown
	return checkDB(f.target().path, f.engine)
}

func (f *siteForm) fields() []int {
	fs := []int{fieldBench, fieldDomain, fieldAdminPW, fieldEngine}
	if len(f.apps) > 0 {
		fs = append(fs, fieldApps)
	}
	if f.engine == "postgres" {
		fs = append(fs, fieldRootUser, fieldRootPW)
	} else if f.engine == "mariadb" {
		fs = append(fs, fieldRootPW)
	}
	if f.exists {
		fs = append(fs, fieldForce)
	}
	return fs
}

func (f *siteForm) setFocus(id int) {
	f.focus = id
	for _, x := range []struct {
		id int
		in *textinput.Model
	}{{fieldDomain, &f.domain}, {fieldAdminPW, &f.adminPW}, {fieldRootUser, &f.rootUser}, {fieldRootPW, &f.rootPW}} {
		if x.id == id {
			x.in.Focus()
		} else {
			x.in.Blur()
		}
	}
}

func (f *siteForm) move(delta int) {
	fs := f.fields()
	i := slices.Index(fs, f.focus)
	if i < 0 {
		i = len(fs) - 1 // the focused field disappeared
	}
	f.setFocus(fs[min(max(i+delta, 0), len(fs)-1)])
}

func (f *siteForm) onLast() bool {
	fs := f.fields()
	return f.focus == fs[len(fs)-1]
}

func (f *siteForm) input() *textinput.Model {
	switch f.focus {
	case fieldDomain:
		return &f.domain
	case fieldAdminPW:
		return &f.adminPW
	case fieldRootUser:
		return &f.rootUser
	case fieldRootPW:
		return &f.rootPW
	}
	return nil
}

// refresh re-reads whether the name is taken on the target bench and, for a
// site not checked yet, starts the check that tells a failed install apart
// from a working site.
func (f *siteForm) refresh() tea.Cmd {
	name, bench := f.name(), f.target().path
	f.exists = core.ValidateSiteName(name) == nil && core.SiteExists(bench, name)
	if !f.exists {
		f.checked, f.check, f.force = "", nil, false
		if !slices.Contains(f.fields(), f.focus) {
			f.move(0)
		}
		return nil
	}
	key := bench + "|" + name
	if f.checked == key {
		return nil
	}
	f.checked, f.check, f.force = key, nil, false
	if f.db == dbDown {
		// Nothing to learn until the database answers; checked again then.
		f.check = &core.SiteCheck{State: core.SiteUnknown, Reason: core.DBLabel(f.engine) + " is not running"}
		return nil
	}
	return checkSiteCmd(bench, name)
}

// ── Drop dialog ─────────────────────────────────────────────────────────────

type dropDialog struct {
	site, engine string
	input        textinput.Model
	err          string
	force        bool
	focusForce   bool
	check        *core.SiteCheck
	db           dbState
	dbWhy        string
}

// archiveOnly reports a site whose database was never created: bench cannot
// drop it, so its folder is archived without touching the database.
func (d *dropDialog) archiveOnly() bool {
	return d.check != nil && d.check.State == core.SiteIncomplete && d.check.DBMissing
}

func (d *dropDialog) setCheck(c core.SiteCheck) {
	d.check = &c
	if c.State == core.SiteIncomplete {
		d.force = true
	}
}

// ── Install app dialog ──────────────────────────────────────────────────────

type installDialog struct {
	site, engine string
	apps         []string // bench apps
	installed    []string // on the site, when a check has told us
	unfinished   string   // why the site cannot take apps: its install did not finish
	at           int
	err          string
	db           dbState
	dbWhy        string
}

func (d *installDialog) isInstalled(app string) bool { return slices.Contains(d.installed, app) }

func (d *installDialog) allInstalled() bool {
	if len(d.apps) == 0 {
		return true
	}
	for _, a := range d.apps {
		if !d.isInstalled(a) {
			return false
		}
	}
	return true
}

// ── Add domain dialog ───────────────────────────────────────────────────────

type domainDialog struct {
	site   string
	domain textinput.Model
	cert   textinput.Model
	key    textinput.Model
	focus  int
	err    string
}

func (d *domainDialog) fields() []int {
	return []int{0, 1, 2}
}

func (d *domainDialog) setFocus(id int) {
	d.focus = id
	for _, x := range []struct {
		id int
		in *textinput.Model
	}{{0, &d.domain}, {1, &d.cert}, {2, &d.key}} {
		if x.id == id {
			x.in.Focus()
		} else {
			x.in.Blur()
		}
	}
}

func (d *domainDialog) input() *textinput.Model {
	switch d.focus {
	case 0:
		return &d.domain
	case 1:
		return &d.cert
	case 2:
		return &d.key
	}
	return nil
}

func (d *domainDialog) move(delta int) {
	d.setFocus(min(max(d.focus+delta, 0), 2))
}

// ── Restore dialog ──────────────────────────────────────────────────────────

const (
	rTarget = iota
	rBackup
	rPath
	rFiles
	rBackupFirst
	rForce
	rRootUser
	rRootPW
)

type restoreDialog struct {
	bench, engine             string
	target, path              textinput.Model
	rootUser, rootPW          textinput.Model
	backups                   []core.Backup
	pick                      int // len(backups) means "another file"
	files, backupFirst, force bool
	focus                     int
	err                       string
	db                        dbState
	dbWhy                     string
	// Derived from the inputs by refresh, so View never touches the disk.
	exists    bool
	sql       string
	pub, priv string
}

func (r *restoreDialog) otherFile() bool { return r.pick == len(r.backups) }

func (r *restoreDialog) refresh() {
	name := strings.TrimSpace(r.target.Value())
	r.exists = core.ValidateSiteName(name) == nil && core.SiteExists(r.bench, name)
	if !r.otherFile() {
		b := r.backups[r.pick]
		r.sql, r.pub, r.priv = b.Database, b.PublicFiles, b.PrivateFiles
		return
	}
	r.sql, _ = core.ExpandHome(strings.TrimSpace(r.path.Value()))
	r.pub, r.priv = core.FilesNextTo(r.sql)
}

func (r *restoreDialog) fields() []int {
	fs := []int{rTarget, rBackup}
	if r.otherFile() {
		fs = append(fs, rPath)
	}
	if r.pub != "" || r.priv != "" {
		fs = append(fs, rFiles)
	}
	if r.exists {
		fs = append(fs, rBackupFirst)
	}
	fs = append(fs, rForce)
	if r.engine == "postgres" {
		fs = append(fs, rRootUser, rRootPW)
	} else if r.engine == "mariadb" {
		fs = append(fs, rRootPW)
	}
	return fs
}

func (r *restoreDialog) setFocus(id int) {
	r.focus = id
	for _, x := range []struct {
		id int
		in *textinput.Model
	}{{rTarget, &r.target}, {rPath, &r.path}, {rRootUser, &r.rootUser}, {rRootPW, &r.rootPW}} {
		if x.id == id {
			x.in.Focus()
		} else {
			x.in.Blur()
		}
	}
}

func (r *restoreDialog) move(delta int) {
	fs := r.fields()
	i := slices.Index(fs, r.focus)
	if i < 0 {
		i = 0
	}
	r.setFocus(fs[min(max(i+delta, 0), len(fs)-1)])
}

func (r *restoreDialog) input() *textinput.Model {
	switch r.focus {
	case rTarget:
		return &r.target
	case rPath:
		return &r.path
	case rRootUser:
		return &r.rootUser
	case rRootPW:
		return &r.rootPW
	}
	return nil
}

// ── Model ───────────────────────────────────────────────────────────────────

// SitesModel lists and manages the sites of the active bench, and creates
// sites on any bench.
type SitesModel struct {
	mgr     *core.Manager
	sup     *core.Supervisor
	bench   core.ActiveBench
	sites   []string
	dbTypes map[string]string
	health  map[string]string // site → "unfinished install" / "last install failed"
	failed  map[string]bool
	port    int // webserver_port, read on reload rather than on every frame
	nav     listNav
	width   int
	height  int
	visible bool
	db      dbState // the bench's database, for the list
	dbWhy   string
	scanned bool // every site was checked since the last reload

	siteApps map[string][]string // apps per site, from the checks

	form    *siteForm
	drop    *dropDialog
	restore *restoreDialog
	install *installDialog
	domain  *domainDialog
}

// NewSitesModel creates the view for bench. mgr supplies the other benches a
// new site can go on and sup starts a stopped database; either may be nil.
func NewSitesModel(mgr *core.Manager, sup *core.Supervisor, bench core.ActiveBench) SitesModel {
	m := SitesModel{mgr: mgr, sup: sup, bench: bench, failed: map[string]bool{}, siteApps: map[string][]string{}}
	m.reload()
	return m
}

func (m *SitesModel) reload() {
	m.sites = core.ListSiteDirs(m.bench.Path)
	m.port = core.WebserverPort(m.bench.Path)
	m.dbTypes = make(map[string]string, len(m.sites))
	m.health = make(map[string]string)
	m.scanned = false
	for _, s := range m.sites {
		m.dbTypes[s] = core.SiteDBType(m.bench.Path, s)
		switch c, ok := core.QuickSiteCheck(m.bench.Path, s); {
		case m.failed[s]:
			m.health[s] = "last install failed"
		case ok && c.State == core.SiteIncomplete:
			m.health[s] = "unfinished install"
		}
	}
	m.nav.fix(len(m.sites), m.listRows())
}

// SetVisible is called on tab changes. Opening the tab checks the database
// and, when it is up, every site in the background, so leftovers of failed
// installs are marked in the list.
func (m *SitesModel) SetVisible(v bool) tea.Cmd {
	was := m.visible
	m.visible = v
	if v && !was {
		return checkDB(m.bench.Path, engineOr(m.bench.Entry.DBEngine))
	}
	return nil
}

// scan checks the sites that files alone cannot judge.
func (m *SitesModel) scan() tea.Cmd {
	m.scanned = true
	var todo []string
	for _, s := range m.sites {
		if _, decided := core.QuickSiteCheck(m.bench.Path, s); !decided {
			todo = append(todo, s)
		}
	}
	return scanSitesCmd(m.bench.Path, todo)
}

// CapturingInput reports whether a dialog owns the keyboard.
func (m SitesModel) CapturingInput() bool {
	return m.form != nil || m.drop != nil || m.restore != nil || m.install != nil || m.domain != nil
}

// SetSize sets the content area.
func (m *SitesModel) SetSize(w, h int) {
	m.width, m.height = w, h
	m.nav.fix(len(m.sites), m.listRows())
}

func (m SitesModel) listRows() int { return max(m.height-7, 1) }

func (m SitesModel) selected() (string, bool) {
	if m.nav.cursor < len(m.sites) {
		return m.sites[m.nav.cursor], true
	}
	return "", false
}

func engineOr(e string) string {
	if e == "" {
		return "mariadb"
	}
	return e
}

// formBenches lists where a new site can go: this session's bench first,
// then every other registered bench whose directory still exists.
func (m SitesModel) formBenches() []siteBench {
	out := []siteBench{{name: m.bench.Name, path: m.bench.Path, engine: engineOr(m.bench.Entry.DBEngine), active: true}}
	if m.mgr == nil {
		return out
	}
	list, _ := m.mgr.ListBenches()
	for _, b := range list {
		if b.Missing || core.SamePath(b.Entry.Path, m.bench.Path) {
			continue
		}
		out = append(out, siteBench{name: b.Name, path: b.Entry.Path, engine: engineOr(b.Entry.DBEngine)})
	}
	return out
}

func (m SitesModel) Update(msg tea.Msg) (SitesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case BenchSwitchedMsg:
		m.bench = msg.Bench
		m.form, m.drop, m.restore, m.install, m.domain = nil, nil, nil, nil, nil
		m.failed = map[string]bool{}
		m.siteApps = map[string][]string{}
		m.reload()
		return m, nil

	case SitesChangedMsg:
		m.reload()
		if m.visible {
			return m, checkDB(m.bench.Path, engineOr(m.bench.Entry.DBEngine))
		}
		return m, nil

	case siteScanMsg:
		if msg.bench != m.bench.Path {
			return m, nil
		}
		switch {
		case m.failed[msg.site]:
		case msg.check.State == core.SiteIncomplete:
			m.health[msg.site] = "unfinished install"
		default:
			delete(m.health, msg.site)
		}
		if msg.check.State == core.SiteHealthy {
			m.siteApps[msg.site] = msg.check.Apps
			if d := m.install; d != nil && d.site == msg.site {
				d.installed = msg.check.Apps
			}
		}
		return m, scanSitesCmd(msg.bench, msg.rest)

	case siteFailedMsg:
		if msg.bench == m.bench.Path && core.SiteExists(msg.bench, msg.site) {
			m.failed[msg.site] = true
			m.reload()
		}
		return m, nil

	case siteCheckMsg:
		if f := m.form; f != nil && f.checked == msg.bench+"|"+msg.site {
			c := msg.check
			f.check = &c
			f.force = c.State == core.SiteIncomplete
		}
		if d := m.drop; d != nil && d.site == msg.site && msg.bench == m.bench.Path {
			d.setCheck(msg.check)
		}
		if d := m.install; d != nil && d.site == msg.site && msg.bench == m.bench.Path && msg.check.State == core.SiteIncomplete {
			d.unfinished = msg.check.Reason
		}
		if msg.bench == m.bench.Path && msg.check.State == core.SiteHealthy {
			m.siteApps[msg.site] = msg.check.Apps
			if d := m.install; d != nil && d.site == msg.site {
				d.installed = msg.check.Apps
			}
		}
		return m, nil

	case dbCheckMsg:
		var cmds []tea.Cmd
		if msg.bench == m.bench.Path && msg.engine == engineOr(m.bench.Entry.DBEngine) {
			m.db, m.dbWhy = msg.state(), msg.why
			if m.db == dbUp && !m.scanned {
				cmds = append(cmds, m.scan())
			}
		}
		if f := m.form; f != nil && msg.bench == f.target().path && msg.engine == f.engine {
			was := f.db
			f.db, f.dbWhy = msg.state(), msg.why
			// A verdict reached while the database was down says nothing.
			if f.db == dbUp && was != dbUp && f.check != nil && f.check.State == core.SiteUnknown {
				f.checked = ""
				cmds = append(cmds, f.refresh())
			}
		}
		if d := m.drop; d != nil && msg.bench == m.bench.Path && msg.engine == d.engine {
			was := d.db
			d.db, d.dbWhy = msg.state(), msg.why
			if d.db == dbUp && was != dbUp && d.check != nil && d.check.State == core.SiteUnknown {
				d.check = nil
				cmds = append(cmds, checkSiteCmd(m.bench.Path, d.site))
			}
		}
		if r := m.restore; r != nil && msg.bench == r.bench && msg.engine == r.engine {
			r.db, r.dbWhy = msg.state(), msg.why
		}
		if d := m.install; d != nil && msg.bench == m.bench.Path && msg.engine == d.engine {
			d.db, d.dbWhy = msg.state(), msg.why
		}
		return m, tea.Batch(cmds...)

	case dbStartedMsg:
		cmds := []tea.Cmd{checkDB(m.bench.Path, engineOr(m.bench.Entry.DBEngine))}
		if f := m.form; f != nil {
			f.checked = "" // check the site again now that the database answers
			cmds = append(cmds, f.checkDB(), f.refresh())
		}
		if d := m.drop; d != nil {
			d.check, d.db = nil, dbUnknown
			cmds = append(cmds, checkDB(m.bench.Path, d.engine), checkSiteCmd(m.bench.Path, d.site))
		}
		if r := m.restore; r != nil {
			r.db = dbUnknown
			cmds = append(cmds, checkDB(r.bench, r.engine))
		}
		if d := m.install; d != nil {
			d.db = dbUnknown
			cmds = append(cmds, checkDB(m.bench.Path, d.engine), checkSiteCmd(m.bench.Path, d.site))
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		switch {
		case m.form != nil:
			return m.updateForm(msg)
		case m.drop != nil:
			return m.updateDrop(msg)
		case m.restore != nil:
			return m.updateRestore(msg)
		case m.install != nil:
			return m.updateInstall(msg)
		case m.domain != nil:
			return m.updateDomain(msg)
		}
		if m.nav.key(msg.String(), len(m.sites), m.listRows()) {
			return m, nil
		}
		site, ok := m.selected()
		switch msg.String() {
		case "ctrl+s":
			if m.db == dbDown {
				return m, startDBJob(m.sup, m.bench.Path, engineOr(m.bench.Entry.DBEngine))
			}
		case "n":
			var cmd tea.Cmd
			m.form, cmd = newSiteForm(m.formBenches())
			return m, cmd
		case "r":
			m.reload()
		case "o":
			if ok {
				return m, openURL(core.SiteURL(m.bench.Path, site))
			}
		case "B":
			if ok {
				spec, err := core.BackupSpec(m.bench.Path, site)
				if err != nil {
					return m, setError(err)
				}
				return m, runJob("Back up "+site, spec, "Backed up "+site+" with its files to "+core.BackupDir(m.bench.Path, site))
			}
		case "R":
			return m, m.openRestore(site)
		case "a":
			if ok {
				return m, func() tea.Msg {
					return SwitchToMarketplaceMsg{TargetSite: site}
				}
			}
		case "d":
			if ok {
				return m, m.openDrop(site)
			}
		case "D", "c":
			if ok {
				return m, m.openDomain(site)
			}
		}
	}
	return m, nil
}

// ── New site ────────────────────────────────────────────────────────────────

func (m SitesModel) updateForm(msg tea.KeyMsg) (SitesModel, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc":
		m.form = nil // drops the typed passwords with it
		return m, nil
	case "ctrl+s":
		if f.db == dbDown {
			return m, startDBJob(m.sup, f.target().path, f.engine)
		}
		return m, nil
	case "tab", "down":
		f.move(1)
		return m, nil
	case "shift+tab", "up":
		f.move(-1)
		return m, nil
	case "enter":
		if !f.onLast() {
			f.move(1)
			return m, nil
		}
		return m.submitForm()
	}

	delta, isToggle := toggleKey(msg.String())
	switch f.focus {
	case fieldBench:
		if !isToggle || len(f.benches) < 2 {
			return m, nil
		}
		f.bench = (f.bench + delta + len(f.benches)) % len(f.benches)
		f.engine = f.target().engine // each bench suggests its own engine
		f.loadApps()
		f.err = ""
		return m, tea.Batch(f.checkDB(), f.refresh())
	case fieldEngine:
		prev := f.engine
		switch msg.String() {
		case "m":
			f.engine = "mariadb"
		case "p":
			f.engine = "postgres"
		default:
			if isToggle {
				f.engine = map[string]string{"mariadb": "postgres", "postgres": "mariadb"}[f.engine]
			}
		}
		if f.engine != prev {
			return m, f.checkDB()
		}
		return m, nil
	case fieldForce:
		if isToggle {
			f.force = !f.force
			f.err = ""
		}
		return m, nil
	case fieldApps:
		switch msg.String() {
		case "left", "h":
			f.appAt = max(f.appAt-1, 0)
		case "right", "l":
			f.appAt = min(f.appAt+1, len(f.apps)-1)
		case " ", "x":
			app := f.apps[f.appAt]
			f.appOn[app] = !f.appOn[app]
		}
		return m, nil
	}
	if in := f.input(); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		f.err = ""
		if f.focus == fieldDomain {
			cmd = tea.Batch(cmd, f.refresh())
		}
		return m, cmd
	}
	return m, nil
}

func (m SitesModel) submitForm() (SitesModel, tea.Cmd) {
	f := m.form
	t, name := f.target(), f.name()
	if f.db == dbDown {
		f.err = core.DBLabel(f.engine) + " is not running for this bench. Press Ctrl+S to start it."
		return m, nil
	}
	if f.exists && !f.force {
		switch {
		case f.check == nil:
			f.err = "Still checking " + name + "…"
		case f.check.State == core.SiteHealthy:
			f.err = name + " already exists and works. Turn on Force to replace it; it is backed up first."
		default:
			f.err = f.check.Describe(name) + ". Turn on Force to replace it."
		}
		f.setFocus(fieldForce)
		return m, nil
	}
	incomplete := f.check != nil && f.check.State == core.SiteIncomplete
	spec, err := core.NewSiteSpec(t.path, core.NewSiteOptions{
		Name:           name,
		AdminPassword:  f.adminPW.Value(),
		DBEngine:       f.engine,
		DBRootUser:     f.rootUser.Value(),
		DBRootPassword: f.rootPW.Value(),
		Force:          f.exists,
		BackupFirst:    f.exists && !incomplete,
		InstallApps:    f.chosenApps(),
	})
	if err != nil {
		f.err = err.Error()
		return m, nil
	}
	m.form = nil
	title := "Create site " + name
	if f.exists {
		title = "Replace site " + name
	}
	success := fmt.Sprintf("Created %s. Open %s", name, core.SiteURL(t.path, name))
	if t.active {
		success += " with [o]"
	} else {
		title += " on bench " + t.name
		success += " once bench " + t.name + " is active"
	}
	success += fmt.Sprintf("; add \"127.0.0.1 %s\" to /etc/hosts if it does not resolve.", name)
	return m, emit(RunJobMsg{
		Title: title, Spec: spec, Success: success,
		After:    []tea.Msg{SitesChangedMsg{}, BenchesChangedMsg{}},
		Failed:   []tea.Msg{siteFailedMsg{bench: t.path, site: name}},
		FailHint: "Press [n] and enter the same name to try again: the half-made site is detected and replaced",
	})
}

// ── Drop ────────────────────────────────────────────────────────────────────

func (m *SitesModel) openDrop(site string) tea.Cmd {
	d := &dropDialog{site: site, engine: engineOr(m.dbTypes[site]), input: newInput("type the site name", 253, false)}
	d.input.Focus()
	m.drop = d
	cmds := []tea.Cmd{checkDB(m.bench.Path, d.engine)}
	if c, ok := core.QuickSiteCheck(m.bench.Path, site); ok {
		d.setCheck(c)
	} else {
		cmds = append(cmds, checkSiteCmd(m.bench.Path, site))
	}
	return tea.Batch(cmds...)
}

func (m SitesModel) updateDrop(msg tea.KeyMsg) (SitesModel, tea.Cmd) {
	d := m.drop
	switch msg.String() {
	case "esc":
		m.drop = nil
		return m, nil
	case "ctrl+s":
		if d.db == dbDown {
			return m, startDBJob(m.sup, m.bench.Path, d.engine)
		}
		return m, nil
	case "tab", "shift+tab", "up", "down":
		d.focusForce = !d.focusForce
		if d.focusForce {
			d.input.Blur()
		} else {
			d.input.Focus()
		}
		return m, nil
	case "enter":
		if strings.TrimSpace(d.input.Value()) != d.site {
			d.err = "That does not match " + d.site + "."
			d.input.SetValue("")
			return m, nil
		}
		if d.db == dbDown && !d.archiveOnly() {
			d.err = core.DBLabel(d.engine) + " is not running. Press Ctrl+S to start it."
			return m, nil
		}
		check := core.SiteCheck{}
		if d.check != nil {
			check = *d.check
		}
		spec, err := core.DropSiteSpec(m.bench.Path, d.site, d.force, check)
		if err != nil {
			return m, setError(err)
		}
		m.drop = nil
		return m, runJob("Drop "+d.site, spec, "Dropped "+d.site+"; it was moved to the bench's archived/sites/", SitesChangedMsg{}, BenchesChangedMsg{})
	}
	if d.focusForce {
		if _, ok := toggleKey(msg.String()); ok {
			d.force = !d.force
		}
		return m, nil
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.err = ""
	return m, cmd
}

// ── Install app ─────────────────────────────────────────────────────────────

func (m *SitesModel) openInstall(site string) tea.Cmd {
	d := &installDialog{site: site, engine: engineOr(m.dbTypes[site]), apps: core.BenchAppNames(m.bench.Path), installed: m.siteApps[site]}
	if c, ok := core.QuickSiteCheck(m.bench.Path, site); ok {
		if c.State == core.SiteIncomplete {
			d.unfinished = c.Reason
		}
		if c.State == core.SiteHealthy && len(c.Apps) > 0 {
			d.installed = c.Apps
			m.siteApps[site] = c.Apps
		}
	}
	for i, a := range d.apps { // start on the first app not installed yet
		if !d.isInstalled(a) {
			d.at = i
			break
		}
	}
	m.install = d
	cmds := []tea.Cmd{checkDB(m.bench.Path, d.engine)}
	if d.installed == nil {
		cmds = append(cmds, checkSiteCmd(m.bench.Path, site))
	}
	return tea.Batch(cmds...)
}

func (m SitesModel) updateInstall(msg tea.KeyMsg) (SitesModel, tea.Cmd) {
	d := m.install
	switch msg.String() {
	case "esc":
		m.install = nil
	case "ctrl+s":
		if d.db == dbDown {
			return m, startDBJob(m.sup, m.bench.Path, d.engine)
		}
	case "up", "k", "left", "h":
		d.at = max(d.at-1, 0)
		d.err = ""
	case "down", "j", "right", "l", "tab":
		d.at = min(d.at+1, max(len(d.apps)-1, 0))
		d.err = ""
	case "enter":
		if d.allInstalled() || len(d.apps) == 0 {
			target := d.site
			m.install = nil
			return m, func() tea.Msg {
				return SwitchToMarketplaceMsg{TargetSite: target}
			}
		}
		app := d.apps[d.at]
		switch {
		case d.unfinished != "":
			d.err = d.site + " did not finish installing. Create it again with [n] first."
			return m, nil
		case d.isInstalled(app):
			d.err = app + " is already installed on " + d.site + "."
			return m, nil
		case d.db == dbDown:
			d.err = core.DBLabel(d.engine) + " is not running. Press Ctrl+S to start it."
			return m, nil
		}
		spec, err := core.InstallAppSpec(m.bench.Path, d.site, app)
		if err != nil {
			d.err = err.Error()
			return m, nil
		}
		m.install = nil
		return m, runJob("Install "+app+" on "+d.site, spec,
			"Installed "+app+" on "+d.site+". Restart services ([r] on Overview) so they load it.", SitesChangedMsg{})
	}
	return m, nil
}

// ── Restore ─────────────────────────────────────────────────────────────────

func (m *SitesModel) openRestore(site string) tea.Cmd {
	r := &restoreDialog{
		bench:       m.bench.Path,
		engine:      engineOr(m.bench.Entry.DBEngine),
		target:      newInput("site to restore into", 253, false),
		path:        newInput("~/Downloads/…-database.sql.gz", 1024, false),
		rootUser:    newInput("postgres", 40, false),
		rootPW:      newInput("blank for trust auth", 64, true),
		files:       true,
		backupFirst: true,
	}
	r.target.SetValue(site)
	if site != "" {
		r.backups = core.ListBackups(m.bench.Path, site)
		r.engine = engineOr(m.dbTypes[site])
	}
	r.refresh()
	if len(r.backups) > 0 {
		r.setFocus(rBackup)
	} else {
		r.setFocus(rPath)
	}
	m.restore = r
	return checkDB(r.bench, r.engine)
}

func (m SitesModel) updateRestore(msg tea.KeyMsg) (SitesModel, tea.Cmd) {
	r := m.restore
	switch msg.String() {
	case "esc":
		m.restore = nil
		return m, nil
	case "ctrl+s":
		if r.db == dbDown {
			return m, startDBJob(m.sup, r.bench, r.engine)
		}
		return m, nil
	case "tab", "down":
		r.move(1)
		return m, nil
	case "shift+tab", "up":
		r.move(-1)
		return m, nil
	case "enter":
		fs := r.fields()
		if r.focus != fs[len(fs)-1] {
			r.move(1)
			return m, nil
		}
		return m.submitRestore()
	}
	delta, isToggle := toggleKey(msg.String())
	switch r.focus {
	case rBackup:
		if isToggle {
			n := len(r.backups) + 1
			r.pick = (r.pick + delta + n) % n
			r.refresh()
			r.err = ""
		}
		return m, nil
	case rFiles, rBackupFirst, rForce:
		if isToggle {
			p := map[int]*bool{rFiles: &r.files, rBackupFirst: &r.backupFirst, rForce: &r.force}[r.focus]
			*p = !*p
		}
		return m, nil
	}
	if in := r.input(); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		r.refresh()
		r.err = ""
		return m, cmd
	}
	return m, nil
}

func (m SitesModel) submitRestore() (SitesModel, tea.Cmd) {
	r := m.restore
	if r.db == dbDown {
		r.err = core.DBLabel(r.engine) + " is not running. Press Ctrl+S to start it."
		return m, nil
	}
	opts := core.RestoreOptions{
		Site:        r.target.Value(),
		SQLPath:     r.sql,
		Force:       r.force,
		BackupFirst: r.backupFirst && r.exists,
	}
	if r.files {
		opts.PublicFiles, opts.PrivateFiles = r.pub, r.priv
	}
	if r.engine == "postgres" {
		opts.DBRootUser = firstNonEmpty(strings.TrimSpace(r.rootUser.Value()), "postgres")
		opts.DBRootPassword = r.rootPW.Value()
	}
	spec, err := core.RestoreSpec(r.bench, opts)
	if err != nil {
		r.err = err.Error()
		return m, nil
	}
	site := strings.TrimSpace(opts.Site)
	m.restore = nil
	return m, emit(RunJobMsg{
		Title: "Restore " + site, Spec: spec,
		Success:  "Restored " + site + ". Restart services ([r] on Overview) if it was running.",
		After:    []tea.Msg{SitesChangedMsg{}, BenchesChangedMsg{}},
		FailHint: "If the backup is from an older Frappe version, turn on Force and try again",
	})
}

// ── View ────────────────────────────────────────────────────────────────────

func (m SitesModel) View(w, h int) string {
	out := lipgloss.JoinVertical(lipgloss.Left,
		panel(w, h-1, m.renderList(w-2, h-3)),
		helpBar(w, "n", "new site", "a", "add app", "o", "open", "B", "backup", "R", "restore", "d", "drop", "D", "domain", "r", "reload"),
	)
	switch {
	case m.form != nil:
		return overlay(out, m.renderForm(), w, h)
	case m.drop != nil:
		return overlay(out, m.renderDrop(), w, h)
	case m.restore != nil:
		return overlay(out, m.renderRestore(), w, h)
	case m.install != nil:
		return overlay(out, m.renderInstall(), w, h)
	case m.domain != nil:
		return overlay(out, m.renderDomain(), w, h)
	}
	return out
}

func (m SitesModel) renderList(w, h int) string {
	title := theme.StylePrimary.Render(fmt.Sprintf("Sites on bench '%s'  (%d)", m.bench.Name, len(m.sites)))
	if m.db == dbDown {
		title += "   " + dbLine(m.db, engineOr(m.bench.Entry.DBEngine), true)
	}
	lines := []string{title}
	if m.db == dbDown && m.dbWhy != "" {
		lines = append(lines, theme.StyleBadgeWarn.Render("⚠ "+m.dbWhy))
	}
	lines = append(lines, theme.StyleMuted.Render(strings.Repeat("─", max(w, 1))))
	if len(m.sites) == 0 {
		lines = append(lines, "", theme.StyleMuted.Render("  No sites yet. Press [n] to create one, or [R] to restore a backup."))
		return strings.Join(lines, "\n")
	}
	nameW := max(min(w-36, 60), 20)
	lines = append(lines, theme.StyleMuted.Render(fmt.Sprintf("  %-*s  %-9s %s", nameW, "SITE", "DATABASE", "STATUS")))
	rows := max(h-4, 1)
	start, end := m.nav.window(len(m.sites), rows)
	for i := start; i < end; i++ {
		site := m.sites[i]
		status := ""
		if s := m.health[site]; s != "" {
			status = "⚠ " + s
		}
		row := fmt.Sprintf("  %-*s  %-9s %s", nameW, pad(site, nameW), m.dbTypes[site], status)
		switch {
		case i == m.nav.cursor:
			lines = append(lines, theme.StyleRowSelected.Render(row))
		case status != "":
			lines = append(lines, theme.StyleBadgeWarn.Render(row))
		default:
			lines = append(lines, row)
		}
	}
	if site, ok := m.selected(); ok {
		hint := fmt.Sprintf("  http://%s:%d", site, m.port)
		if domains := core.SiteDomains(m.bench.Path, site); len(domains) > 0 {
			hint += " · domains: " + strings.Join(domains, ", ")
		}
		if apps := m.siteApps[site]; len(apps) > 0 {
			hint += " · apps: " + strings.Join(apps, ", ")
		}
		if m.health[site] != "" {
			hint = "  Drop it with [d], or create it again with [n]: vybench replaces a half-made site."
		}
		lines = append(lines, theme.StyleMuted.Render(hint)+scrollHint(start, end, len(m.sites)))
	}
	return strings.Join(lines, "\n")
}

func (m SitesModel) renderForm() string {
	f := m.form
	t, name := f.target(), f.name()
	rows := []string{formRow(f.focus == fieldBench, "Bench", f.benchChoice())}
	if f.focus == fieldBench {
		rows = append(rows, formNote(theme.StyleMuted.Render(truncateLeft(t.path, 44))))
	}
	rows = append(rows, dbRows(f.db, f.dbWhy, f.engine)...)
	rows = append(rows, formRow(f.focus == fieldDomain, "Domain", f.domain.View()))
	if f.exists {
		switch {
		case f.check == nil:
			rows = append(rows, formNote(theme.StyleMuted.Render("checking "+name+"…")))
		case f.check.State == core.SiteHealthy:
			rows = append(rows, formNote(theme.StyleBadgeWarn.Render("⚠ already exists and works")))
		case f.check.State == core.SiteIncomplete:
			rows = append(rows, formNotes(theme.StyleBadgeWarn, "⚠ left over from an install that did not finish: "+f.check.Reason)...)
		case f.db == dbDown:
			rows = append(rows, formNote(theme.StyleBadgeWarn.Render("⚠ exists; checked once "+core.DBLabel(f.engine)+" runs")))
		default:
			rows = append(rows, formNotes(theme.StyleBadgeWarn, "⚠ exists; could not check it: "+f.check.Reason)...)
		}
	}
	rows = append(rows,
		formRow(f.focus == fieldAdminPW, "Admin password", f.adminPW.View()),
		formRow(f.focus == fieldEngine, "Database", engineChoice(f.engine)),
	)
	if len(f.apps) > 0 {
		rows = append(rows, formRow(f.focus == fieldApps, "Apps", f.appsChoice()))
	}
	if f.engine == "postgres" {
		rows = append(rows,
			formRow(f.focus == fieldRootUser, "PG superuser", f.rootUser.View()),
			formRow(f.focus == fieldRootPW, "PG password", f.rootPW.View()),
		)
	} else if f.engine == "mariadb" {
		rows = append(rows,
			formRow(f.focus == fieldRootPW, "DB root PW", f.rootPW.View()),
		)
	}
	if f.exists {
		note := " · backed up first"
		if f.check != nil && f.check.State == core.SiteIncomplete {
			note = " · recreates the unfinished site"
		}
		rows = append(rows, formRow(f.focus == fieldForce, "Force", checkbox(f.force)+" replace "+name+theme.StyleMuted.Render(note)))
	}
	if f.err != "" {
		rows = append(rows, "", errorText(f.err))
	}
	keys := theme.StyleMuted.Render("[Tab/Enter] Next  [Shift+Tab] Back  [Esc] Cancel")
	switch {
	case f.focus == fieldApps && !f.onLast():
		keys = theme.StyleMuted.Render("[←→] App  [Space] Toggle  [Tab] Next · more apps: [3] Marketplace")
	case f.onLast():
		verb := "[Enter] Create site"
		if f.exists {
			verb = "[Enter] Replace site"
		}
		keys = theme.StylePrimary.Render(verb) + theme.StyleMuted.Render("  [Tab] Fields  [Esc] Cancel")
	}
	return dialog("Create New Site", rows, keys)
}

// appsChoice renders the bench's apps as checkboxes, the one under the
// cursor highlighted while the row is focused.
func (f *siteForm) appsChoice() string {
	var parts []string
	for i, a := range f.apps {
		item := checkbox(f.appOn[a]) + " " + a
		if f.focus == fieldApps && i == f.appAt {
			item = theme.StyleRowSelected.Render(ansiStrip(item))
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, "  ")
}

// benchChoice renders the bench selector: the chosen bench, whether it is
// the active one, and how to pick another.
func (f *siteForm) benchChoice() string {
	t := f.target()
	name := theme.StyleBold.Render(t.name)
	note := ""
	if t.active {
		note = "active"
	}
	if len(f.benches) > 1 {
		name = theme.StyleMuted.Render("◀ ") + name + theme.StyleMuted.Render(" ▶")
		if note != "" {
			note += " · "
		}
		note += fmt.Sprintf("%d of %d · ←→", f.bench+1, len(f.benches))
	}
	if note != "" {
		name += theme.StyleMuted.Render("  " + note)
	}
	return name
}

func (m SitesModel) renderDrop() string {
	d := m.drop
	var rows []string
	switch {
	case d.check == nil:
		rows = append(rows, theme.StyleMuted.Render("Checking "+d.site+"…"))
	case d.archiveOnly():
		rows = append(rows, theme.StyleBadgeWarn.Render("⚠ Its install did not finish and it has no database."),
			"Its folder is moved to archived/sites/; nothing else to remove.")
	case d.check.State == core.SiteIncomplete:
		rows = append(rows, theme.StyleBadgeWarn.Render("⚠ Its install did not finish ("+d.check.Reason+")."),
			"bench drops its database and archives the folder.")
	case d.check.State == core.SiteUnknown && d.db == dbDown:
		rows = append(rows, theme.StyleBadgeWarn.Render("⚠ It can be checked once "+core.DBLabel(d.engine)+" runs."))
	case d.check.State == core.SiteUnknown:
		rows = append(rows, theme.StyleBadgeWarn.Render("⚠ Could not check it: "+truncateLeft(d.check.Reason, 40)))
	default:
		rows = append(rows, "bench backs it up, drops its database and moves",
			"its folder to archived/sites/.")
	}
	if !d.archiveOnly() {
		rows = append(rows, "")
		rows = append(rows, dbRows(d.db, d.dbWhy, d.engine)...)
	}
	rows = append(rows,
		"",
		"Type "+theme.StyleBold.Render(d.site)+" to confirm:",
		"  "+d.input.View(),
		formRow(d.focusForce, "Force", checkbox(d.force)+" drop even if its backup fails"),
	)
	if d.err != "" {
		rows = append(rows, errorText(d.err))
	}
	return dialog(theme.StyleLogError.Render("Drop site "+d.site), rows,
		theme.StyleMuted.Render("[Enter] Drop  [Tab] Force  [Esc] Cancel"))
}

func (m SitesModel) renderInstall() string {
	d := m.install
	if d.installed == nil {
		if c, ok := core.QuickSiteCheck(m.bench.Path, d.site); ok && c.State == core.SiteHealthy && len(c.Apps) > 0 {
			d.installed = c.Apps
		}
	}
	var rows []string
	if d.unfinished != "" {
		rows = append(rows, theme.StyleBadgeWarn.Render("⚠ This site did not finish installing ("+d.unfinished+")."),
			theme.StyleMuted.Render("Create it again with [n]; ticked apps are installed with it."), "")
	}
	if d.allInstalled() {
		rows = append(rows,
			fmt.Sprintf("All local bench apps are installed on %s.", d.site),
			"",
			theme.StylePrimary.Render("[Enter] Browse Marketplace to install new apps"),
		)
		rows = append(rows, "")
		rows = append(rows, dbRows(d.db, d.dbWhy, d.engine)...)
		rows = append(rows, theme.StyleMuted.Render("Apps not in this bench: [3] Marketplace installs them."))
		if d.err != "" {
			rows = append(rows, "", errorText(d.err))
		}
		return dialog("Install App on "+d.site, rows,
			theme.StylePrimary.Render("[Enter] Marketplace")+theme.StyleMuted.Render("  [Esc] Cancel"))
	}
	if len(d.apps) == 0 {
		rows = append(rows, theme.StyleMuted.Render("This bench has no apps besides frappe."))
	}
	for i, a := range d.apps {
		mark := "  "
		if i == d.at {
			mark = theme.StylePrimary.Render("▶ ")
		}
		line := mark + fmt.Sprintf("%-18s", a)
		switch {
		case d.isInstalled(a):
			line += theme.StyleLogOK.Render("✓ installed")
		case d.installed == nil && d.db != dbDown:
			line += theme.StyleMuted.Render("checking…")
		}
		rows = append(rows, line)
	}
	rows = append(rows, "")
	rows = append(rows, dbRows(d.db, d.dbWhy, d.engine)...)
	rows = append(rows, theme.StyleMuted.Render("Apps not in this bench: [3] Marketplace installs them."))
	if d.err != "" {
		rows = append(rows, "", errorText(d.err))
	}
	return dialog("Install App on "+d.site, rows,
		theme.StylePrimary.Render("[Enter] Install")+theme.StyleMuted.Render("  [↑↓] Choose  [Esc] Cancel"))
}

func (m SitesModel) renderRestore() string {
	r := m.restore
	target := strings.TrimSpace(r.target.Value())
	rows := []string{formRow(r.focus == rTarget, "Into site", r.target.View())}
	switch {
	case target == "":
	case r.exists:
		rows = append(rows, formNote(theme.StyleBadgeWarn.Render("⚠ replaces all data in "+target)))
	case core.ValidateSiteName(target) == nil:
		rows = append(rows, formNote(theme.StyleLogOK.Render("✔ creates a new site")))
	}

	var pick string
	switch {
	case r.otherFile() && len(r.backups) == 0:
		pick = theme.StyleMuted.Render("no backups of this site yet; enter a file")
	case r.otherFile():
		pick = theme.StyleMuted.Render("◀ ") + "another file…" + theme.StyleMuted.Render(" ▶")
	default:
		pick = theme.StyleMuted.Render("◀ ") + r.backups[r.pick].Label() + theme.StyleMuted.Render(
			fmt.Sprintf(" ▶  %d of %d", r.pick+1, len(r.backups)))
	}
	rows = append(rows, formRow(r.focus == rBackup, "Backup", pick))
	if r.otherFile() {
		rows = append(rows, formRow(r.focus == rPath, "File", r.path.View()))
	}
	rows = append(rows, dbRows(r.db, r.dbWhy, r.engine)...)
	if r.pub != "" || r.priv != "" {
		rows = append(rows, formRow(r.focus == rFiles, "Files", checkbox(r.files)+" restore public and private files"))
	}
	if r.exists {
		rows = append(rows, formRow(r.focus == rBackupFirst, "Back up first", checkbox(r.backupFirst)+" back up "+target+" before restoring"))
	}
	rows = append(rows, formRow(r.focus == rForce, "Force", checkbox(r.force)+" allow a backup from an older Frappe"))
	if r.engine == "postgres" {
		rows = append(rows,
			formRow(r.focus == rRootUser, "PG superuser", r.rootUser.View()),
			formRow(r.focus == rRootPW, "PG password", r.rootPW.View()),
		)
	} else if r.engine == "mariadb" {
		rows = append(rows,
			formRow(r.focus == rRootPW, "DB root PW", r.rootPW.View()),
		)
	}
	if r.err != "" {
		rows = append(rows, "", errorText(r.err))
	}
	// Enter only submits on the last field (see updateRestore); everywhere else
	// it moves on. Labelling it "Restore" throughout, for an action that
	// replaces every row in a site, made the dialog read as though the first
	// Enter would go through -- the new-site form gets this right.
	fs := r.fields()
	keys := theme.StyleMuted.Render("[Tab/Enter] Next  [←→] Choose  [Space] Toggle  [Shift+Tab] Back  [Esc] Cancel")
	if r.focus == fs[len(fs)-1] {
		keys = theme.StylePrimary.Render("[Enter] Restore") + theme.StyleMuted.Render("  [Tab] Fields  [Esc] Cancel")
	}
	return dialog("Restore Backup", rows, keys)
}

// engineChoice renders the MariaDB / PostgreSQL toggle.
func engineChoice(engine string) string {
	maria, pg := theme.StyleMuted, theme.StyleMuted
	if engine == "postgres" {
		pg = theme.StyleBadgePostgres
	} else {
		maria = theme.StyleBadgeMariaDB
	}
	return maria.Render("[M] MariaDB") + "  " + pg.Render("[P] PostgreSQL")
}

// ── Domain ──────────────────────────────────────────────────────────────────

func (m *SitesModel) openDomain(site string) tea.Cmd {
	d := &domainDialog{
		site:   site,
		domain: newInput("custom.example.com", 253, false),
		cert:   newInput("/path/to/cert.pem (optional)", 1024, false),
		key:    newInput("/path/to/key.pem (optional)", 1024, false),
	}
	d.domain.Focus()
	m.domain = d
	return nil
}

func (m SitesModel) updateDomain(msg tea.KeyMsg) (SitesModel, tea.Cmd) {
	d := m.domain
	switch msg.String() {
	case "esc":
		m.domain = nil
		return m, nil
	case "tab", "down":
		d.move(1)
		return m, nil
	case "shift+tab", "up":
		d.move(-1)
		return m, nil
	case "enter":
		if d.focus != 2 {
			d.move(1)
			return m, nil
		}
		domain := strings.TrimSpace(d.domain.Value())
		if err := core.ValidateDomainName(domain); err != nil {
			d.err = err.Error()
			return m, nil
		}
		existing := core.SiteDomains(m.bench.Path, d.site)
		if slices.Contains(existing, domain) {
			d.err = domain + " is already registered on " + d.site
			return m, nil
		}
		opts := core.AddDomainOptions{
			Site:              d.site,
			Domain:            domain,
			SSLCertificate:    strings.TrimSpace(d.cert.Value()),
			SSLCertificateKey: strings.TrimSpace(d.key.Value()),
		}
		spec, err := core.AddDomainSpec(m.bench.Path, opts)
		if err != nil {
			d.err = err.Error()
			return m, nil
		}
		m.domain = nil
		return m, emit(RunJobMsg{
			Title:   "Add domain " + domain + " to " + d.site,
			Spec:    spec,
			Success: "Added custom domain " + domain + " to " + d.site,
			After:   []tea.Msg{SitesChangedMsg{}},
		})
	}
	if in := d.input(); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		d.err = ""
		return m, cmd
	}
	return m, nil
}

func (m SitesModel) renderDomain() string {
	d := m.domain
	rows := []string{
		"Site: " + theme.StyleBold.Render(d.site),
	}
	existing := core.SiteDomains(m.bench.Path, d.site)
	if len(existing) > 0 {
		rows = append(rows, "Registered domains: "+strings.Join(existing, ", "))
	}
	rows = append(rows,
		"",
		formRow(d.focus == 0, "Domain", d.domain.View()),
		formRow(d.focus == 1, "SSL Cert", d.cert.View()),
		formRow(d.focus == 2, "SSL Key", d.key.View()),
	)
	if d.err != "" {
		rows = append(rows, "", errorText(d.err))
	}
	return dialog("Add Custom Domain to "+d.site, rows,
		theme.StylePrimary.Render("[Enter] Submit")+theme.StyleMuted.Render("  [Tab] Fields  [Esc] Cancel"))
}

