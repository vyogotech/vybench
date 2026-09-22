package views

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

// sitesFixture is a bench with one working and one half-made site, and a
// fake vybench CLI.
func sitesFixture(t *testing.T) (SitesModel, string) {
	t.Helper()
	fakeCLI(t)
	bench := t.TempDir()
	write(t, filepath.Join(bench, "sites", "common_site_config.json"), "{}")
	write(t, filepath.Join(bench, "sites", "good.localhost", "site_config.json"), `{"db_name": "_good"}`)
	write(t, filepath.Join(bench, "sites", "half.localhost", "site_config.json"), `{"db_name": "_half"}`)
	m := NewSitesModel(nil, nil, core.ActiveBench{Name: "b", Path: bench, Entry: core.BenchEntry{DBEngine: "mariadb"}})
	m.SetSize(100, 30)
	return m, bench
}

func dbIs(m SitesModel, bench string, up bool) SitesModel {
	m, _ = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: up})
	return m
}

func openForm(t *testing.T, m SitesModel, name string) SitesModel {
	t.Helper()
	m, _ = m.Update(press("n"))
	m = dbIs(m, m.form.target().path, true)
	m.form.domain.SetValue(name)
	m.form.refresh()
	m.form.adminPW.SetValue("pw")
	return m
}

func submit(t *testing.T, m SitesModel) (SitesModel, []tea.Msg) {
	t.Helper()
	m.form.setFocus(m.form.fields()[len(m.form.fields())-1])
	m, cmd := m.Update(press("enter"))
	return m, run(cmd)
}

func TestNewSiteRejectsBadNames(t *testing.T) {
	m, _ := sitesFixture(t)
	m = openForm(t, m, "Bad Name")
	m, _ = submit(t, m)
	if m.form == nil || !strings.Contains(m.form.err, "invalid site name") {
		t.Fatalf("invalid domain accepted: %+v", m.form)
	}
}

func TestNewSiteCreatesAFreshSite(t *testing.T) {
	m, bench := sitesFixture(t)
	m = openForm(t, m, "new.localhost")
	if m.form.exists || slices.Contains(m.form.fields(), fieldForce) {
		t.Error("Force offered for a name that is free")
	}
	m, msgs := submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok || m.form != nil || job.Title != "Create site new.localhost" {
		t.Fatalf("job %+v form %v", job, m.form)
	}
	args := job.Spec.Steps[0].Cmd.Args
	if !slices.Contains(args, "new-site") || slices.Contains(args, "--force") || job.Spec.Steps[0].Cmd.Dir != bench {
		t.Errorf("args %v dir %s", args, job.Spec.Steps[0].Cmd.Dir)
	}
	if _, ok := find[siteFailedMsg](job.Failed); !ok || job.FailHint == "" {
		t.Error("a failed creation is not reported back to the list")
	}
}

// The screenshot case: the database is not running.
func TestNewSiteWaitsForTheDatabase(t *testing.T) {
	m, bench := sitesFixture(t)
	m = openForm(t, m, "new.localhost")
	m = dbIs(m, bench, false)
	if !strings.Contains(ansi.Strip(m.renderForm()), "MariaDB is not running") {
		t.Error("the form does not say the database is down")
	}
	m, msgs := submit(t, m)
	if _, ok := find[RunJobMsg](msgs); ok || !strings.Contains(m.form.err, "Ctrl+S") {
		t.Fatalf("created a site with the database down; err %q", m.form.err)
	}
	// Once the database is back, the form checks again.
	m, cmd := m.Update(dbStartedMsg{})
	if m.form.db != dbUnknown || cmd == nil {
		t.Error("dbStartedMsg did not trigger a new check")
	}
}

// A leftover from a failed install is replaced in place, reusing its
// database name, without asking for Force and without a backup.
func TestNewSiteReplacesAFailedInstall(t *testing.T) {
	m, bench := sitesFixture(t)
	m = openForm(t, m, "half.localhost")
	if !m.form.exists || m.form.check != nil {
		t.Fatalf("existing site not detected: exists %v", m.form.exists)
	}
	m, _ = m.Update(siteCheckMsg{bench: bench, site: "half.localhost",
		check: core.SiteCheck{State: core.SiteIncomplete, Reason: "its database was never created", DBMissing: true}})
	if !m.form.force {
		t.Fatal("Force was not turned on for an unfinished install")
	}
	if !strings.Contains(ansi.Strip(m.renderForm()), "left over from an install") {
		t.Error("the form does not explain the leftover")
	}
	m, msgs := submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok || !strings.HasPrefix(job.Title, "Replace site") || len(job.Spec.Steps) != 1 {
		t.Fatalf("job %+v", job)
	}
	args := job.Spec.Steps[0].Cmd.Args
	i := slices.Index(args, "--db-name")
	if !slices.Contains(args, "--force") || i < 0 || args[i+1] != "_half" {
		t.Errorf("args = %v", args)
	}
}

// A working site is only replaced with Force switched on by hand, and it is
// backed up first.
func TestNewSiteKeepsAWorkingSite(t *testing.T) {
	m, bench := sitesFixture(t)
	m = openForm(t, m, "good.localhost")
	m, _ = m.Update(siteCheckMsg{bench: bench, site: "good.localhost", check: core.SiteCheck{State: core.SiteHealthy}})
	m, msgs := submit(t, m)
	if _, ok := find[RunJobMsg](msgs); ok || !strings.Contains(m.form.err, "already exists and works") {
		t.Fatalf("a working site was replaced without Force; err %q", m.form.err)
	}
	if m.form.focus != fieldForce {
		t.Error("focus did not move to Force")
	}
	view := ansi.Strip(m.renderForm())
	if strings.Contains(view, "[Enter] Replace site") {
		t.Fatalf("footer promised Replace while Force was off:\n%s", view)
	}
	if !strings.Contains(view, "Force, then Enter to replace") {
		t.Fatalf("footer should say to turn on Force first:\n%s", view)
	}
	m, _ = m.Update(press(" "))
	view = ansi.Strip(m.renderForm())
	if !strings.Contains(view, "[Enter] Replace site") {
		t.Fatalf("footer should say Replace once Force is on:\n%s", view)
	}
	m, msgs = submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok || len(job.Spec.Steps) != 2 {
		t.Fatalf("job %+v", job)
	}
	if backup := job.Spec.Steps[0].Cmd.Args; !slices.Contains(backup, "backup") || !slices.Contains(backup, "--with-files") {
		t.Errorf("first step is not a backup: %v", backup)
	}
	if !slices.Contains(job.Spec.Steps[1].Cmd.Args, "--force") {
		t.Error("second step does not force")
	}
}

// The database being down is not an installation failure: never force.
func TestNewSiteDoesNotForceOnUnknown(t *testing.T) {
	m, bench := sitesFixture(t)
	m = openForm(t, m, "good.localhost")
	m, _ = m.Update(siteCheckMsg{bench: bench, site: "good.localhost",
		check: core.SiteCheck{State: core.SiteUnknown, Reason: "Can't connect to local server"}})
	if m.form.force {
		t.Error("Force turned on although the site could not be checked")
	}
}

func TestNewSiteOnAnotherBench(t *testing.T) {
	mgr := testManager(t)
	for _, name := range []string{"main", "other"} {
		if err := mgr.CreateBench(core.NewBenchOptions{Name: name, SwitchToNew: name == "main",
			DBEngine: map[string]string{"main": "mariadb", "other": "postgres"}[name]}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewSitesModel(mgr, nil, mgr.ResolveActive())
	m.SetSize(100, 30)
	m, _ = m.Update(press("n"))
	if m.form.focus != fieldBench || len(m.form.benches) != 2 || m.form.target().name != "main" {
		t.Fatalf("form opened on %+v, focus %d", m.form.target(), m.form.focus)
	}
	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "1 of 2") {
		t.Errorf("bench selector not shown:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.form.target().name != "other" || m.form.engine != "postgres" {
		t.Fatalf("after →: bench %q engine %q", m.form.target().name, m.form.engine)
	}
	other := filepath.Join(mgr.VarDir, "benches", "other")
	m, _ = m.Update(dbCheckMsg{bench: other, engine: "postgres", up: true})
	m.form.domain.SetValue("shop.localhost")
	m.form.adminPW.SetValue("pw")
	m, msgs := submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok || job.Spec.Steps[0].Cmd.Dir != other || !strings.Contains(job.Title, "on bench other") {
		t.Fatalf("job %+v", job)
	}
	if _, ok := find[BenchesChangedMsg](job.After); !ok {
		t.Error("the Benches tab is not told its site counts changed")
	}
}

func TestDropNeedsTypedNameAndChecksTheSite(t *testing.T) {
	m, bench := sitesFixture(t)
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, _ = m.Update(press("d"))
	m, _ = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	m, _ = m.Update(siteCheckMsg{bench: bench, site: "good.localhost", check: core.SiteCheck{State: core.SiteHealthy}})
	m = typeText(m, "wrong")
	m, cmd := m.Update(press("enter"))
	if cmd != nil || m.drop == nil || !strings.Contains(ansi.Strip(m.View(100, 30)), "does not match") {
		t.Fatal("a mismatched name must keep the dialog open with an error")
	}
	m = typeText(m, "good.localhost")
	_, cmd = m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok || slices.Contains(job.Spec.Steps[0].Cmd.Args, "--force") {
		t.Errorf("job %+v", job)
	}
}

// A site whose database was never created cannot be dropped by bench; its
// folder is archived instead.
func TestDropArchivesASiteWithoutDatabase(t *testing.T) {
	m, bench := sitesFixture(t)
	// frappe made the folders, then failed before writing site_config.json.
	if err := os.Remove(filepath.Join(bench, "sites", "half.localhost", "site_config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bench, "sites", "half.localhost", "private", "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.reload()
	m.nav.cursor = slices.Index(m.sites, "half.localhost")
	if m.nav.cursor < 0 || m.health["half.localhost"] == "" {
		t.Fatalf("the list does not show the unfinished site: %v %v", m.sites, m.health)
	}
	m, _ = m.Update(press("d"))
	if !m.drop.archiveOnly() || !m.drop.force {
		t.Fatalf("drop dialog %+v", m.drop)
	}
	m = typeText(m, "half.localhost")
	_, cmd := m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok || job.Spec.Steps[0].Fn == nil {
		t.Fatalf("job %+v", job)
	}
	if err := job.Spec.Steps[0].Fn(func(string) {}); err != nil {
		t.Fatal(err)
	}
	if core.SiteExists(bench, "half.localhost") {
		t.Error("the folder was not archived")
	}
	archived, _ := filepath.Glob(filepath.Join(bench, "archived", "sites", "half.localhost-*"))
	if len(archived) != 1 {
		t.Errorf("archive = %v", archived)
	}
}

func TestRestoreFromALocalBackup(t *testing.T) {
	m, bench := sitesFixture(t)
	dir := core.BackupDir(bench, "good.localhost")
	write(t, filepath.Join(dir, "20260910_101010-good_localhost-database.sql.gz"), "old")
	write(t, filepath.Join(dir, "20260911_121212-good_localhost-database.sql.gz"), "new")
	write(t, filepath.Join(dir, "20260911_121212-good_localhost-files.tar"), "")
	write(t, filepath.Join(dir, "20260911_121212-good_localhost-private-files.tar"), "")

	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, _ = m.Update(press("R"))
	r := m.restore
	if r == nil || len(r.backups) != 2 || r.pick != 0 || r.focus != rBackup {
		t.Fatalf("restore dialog %+v", r)
	}
	m, _ = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	view := ansi.Strip(m.View(100, 30))
	for _, want := range []string{"Restore Backup", "replaces all data in good.localhost", "with files", "Back up first"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// Restore into a new site instead, with Force.
	r.setFocus(rTarget)
	r.target.SetValue("copy.localhost")
	r.refresh()
	r.force = true
	r.setFocus(r.fields()[len(r.fields())-1])
	_, cmd := m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok || len(job.Spec.Steps) != 1 {
		t.Fatalf("job %+v", job)
	}
	args := job.Spec.Steps[0].Cmd.Args
	i := slices.Index(args, "--site")
	want := []string{"--site", "copy.localhost", "restore", filepath.Join(dir, "20260911_121212-good_localhost-database.sql.gz"),
		"--with-public-files", filepath.Join(dir, "20260911_121212-good_localhost-files.tar"),
		"--with-private-files", filepath.Join(dir, "20260911_121212-good_localhost-private-files.tar"), "--force"}
	if i < 0 || !slices.Equal(args[i:], want) {
		t.Errorf("args = %v", args[max(i, 0):])
	}
}

func TestRestoreFromAFileWithoutBackups(t *testing.T) {
	m, bench := sitesFixture(t)
	elsewhere := t.TempDir()
	sql := filepath.Join(elsewhere, "20260101_000000-prod_example_com-database.sql.gz")
	write(t, sql, "x")
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, _ = m.Update(press("R"))
	m, _ = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	if m.restore.focus != rPath || !m.restore.otherFile() {
		t.Fatalf("with no backups the file field should be focused: %+v", m.restore)
	}
	m = typeText(m, sql)
	m.restore.setFocus(m.restore.fields()[len(m.restore.fields())-1])
	_, cmd := m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok || len(job.Spec.Steps) != 2 {
		t.Fatalf("expected a safety backup then the restore: %+v", job)
	}
	if args := job.Spec.Steps[1].Cmd.Args; !slices.Contains(args, sql) || slices.Contains(args, "--with-public-files") {
		t.Errorf("args = %v", args)
	}
}

func TestSitesTabScansSitesWhenTheDatabaseIsUp(t *testing.T) {
	m, bench := sitesFixture(t)
	if cmd := m.SetVisible(true); cmd == nil {
		t.Fatal("opening the tab does not check the database")
	}
	m, cmd := m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: false})
	if cmd != nil || m.scanned {
		t.Error("scanned sites with the database down")
	}
	if !strings.Contains(ansi.Strip(m.View(100, 30)), "MariaDB is not running") {
		t.Error("the list does not say the database is down")
	}
	m, cmd = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	if cmd == nil || !m.scanned {
		t.Fatal("no scan once the database is up")
	}
	m, _ = m.Update(siteScanMsg{bench: bench, site: "half.localhost",
		check: core.SiteCheck{State: core.SiteIncomplete, Reason: "its database was never created"}, rest: nil})
	if m.health["half.localhost"] != "unfinished install" {
		t.Errorf("health = %v", m.health)
	}
	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "⚠ unfinished install") {
		t.Errorf("the list does not mark it:\n%s", view)
	}
}

func TestNewSiteChecksAgainOnceTheDatabaseIsUp(t *testing.T) {
	m, bench := sitesFixture(t)
	m, _ = m.Update(press("n"))
	m = dbIs(m, bench, false)
	m.form.domain.SetValue("half.localhost")
	if cmd := m.form.refresh(); cmd != nil {
		t.Error("ran a site check with the database down")
	}
	if !strings.Contains(ansi.Strip(m.renderForm()), "checked once MariaDB runs") {
		t.Error("the form does not say when the site will be checked")
	}
	_, cmd := m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	if cmd == nil {
		t.Error("no new check once the database came up")
	}
}

func TestNewSiteInstallsBenchApps(t *testing.T) {
	m, bench := sitesFixture(t)
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\nerpnext\nhrms\n")
	m = openForm(t, m, "shop.localhost")
	if !slices.Equal(m.form.apps, []string{"erpnext", "hrms"}) || !m.form.appOn["erpnext"] {
		t.Fatalf("apps %v on %v", m.form.apps, m.form.appOn)
	}
	m.form.setFocus(fieldApps)
	m, _ = m.Update(press("l"))
	m, _ = m.Update(press(" ")) // tick hrms
	m, msgs := submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok {
		t.Fatal("no job")
	}
	args := strings.Join(job.Spec.Steps[0].Cmd.Args, " ")
	if !strings.Contains(args, "--install-app erpnext --install-app hrms") {
		t.Errorf("args = %s", args)
	}
}

func TestSitesPressASwitchesToMarketplace(t *testing.T) {
	m, _ := sitesFixture(t)
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, cmd := m.Update(press("a"))
	if cmd == nil {
		t.Fatal("expected command from pressing 'a'")
	}
	msg := cmd()
	sw, ok := msg.(SwitchToMarketplaceMsg)
	if !ok || sw.TargetSite != "good.localhost" {
		t.Fatalf("expected SwitchToMarketplaceMsg with target 'good.localhost', got %+v", msg)
	}
}

func TestInstallAppOnASite(t *testing.T) {
	m, bench := sitesFixture(t)
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\nerpnext\nhrms\n")
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	cmd := m.openInstall("good.localhost")
	if m.install == nil || cmd == nil {
		t.Fatal("no install dialog")
	}
	m, _ = m.Update(dbCheckMsg{bench: bench, engine: "mariadb", up: true})
	m, _ = m.Update(siteCheckMsg{bench: bench, site: "good.localhost",
		check: core.SiteCheck{State: core.SiteHealthy, Apps: []string{"frappe", "erpnext"}}})
	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "✓ installed") {
		t.Errorf("installed apps not marked:\n%s", view)
	}
	m.install.at = 0 // erpnext
	m, _ = m.Update(press("enter"))
	if m.install == nil || !strings.Contains(m.install.err, "already installed") {
		t.Fatalf("installed erpnext again: %+v", m.install)
	}
	m, _ = m.Update(press("down"))
	_, cmd = m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	i := slices.Index(job.Spec.Steps[0].Cmd.Args, "--site")
	if !ok || i < 0 || !slices.Equal(job.Spec.Steps[0].Cmd.Args[i:], []string{"--site", "good.localhost", "install-app", "hrms"}) {
		t.Errorf("job %+v", job)
	}
}

func TestInstallAppAllInstalledSwitchesToMarketplace(t *testing.T) {
	m, bench := sitesFixture(t)
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\nerpnext\n")
	write(t, filepath.Join(bench, "sites", "good.localhost", "site_config.json"), `{"db_name": "_good", "installed_apps": ["frappe", "erpnext"]}`)
	_ = m.openInstall("good.localhost")
	if m.install == nil {
		t.Fatal("expected install dialog")
	}
	if !slices.Equal(m.install.installed, []string{"frappe", "erpnext"}) {
		t.Fatalf("expected installed apps populated, got %v", m.install.installed)
	}
	view := ansi.Strip(m.View(100, 30))
	if !strings.Contains(view, "All local bench apps are installed on good.localhost.") {
		t.Errorf("view missing all installed prompt:\n%s", view)
	}
	m, enterCmd := m.Update(press("enter"))
	if m.install != nil {
		t.Error("dialog should close on enter")
	}
	if enterCmd == nil {
		t.Fatal("expected command on enter")
	}
	msg := enterCmd()
	sw, ok := msg.(SwitchToMarketplaceMsg)
	if !ok || sw.TargetSite != "good.localhost" {
		t.Fatalf("expected SwitchToMarketplaceMsg on enter, got %+v", msg)
	}
}

func TestInstallAppEdgeCases(t *testing.T) {
	m, bench := sitesFixture(t)
	// 1. openInstall on an incomplete site
	_ = m.openInstall("half.localhost")
	if m.install == nil {
		t.Fatal("expected install dialog on half.localhost")
	}
	m.install.unfinished = "db missing"
	viewUnfinished := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewUnfinished, "This site did not finish installing") {
		t.Errorf("expected unfinished warning in view: %s", viewUnfinished)
	}

	// 2. allInstalled with an error message
	m.install.apps = []string{"erpnext"}
	m.install.installed = []string{"erpnext"}
	m.install.err = "marketplace connection error"
	viewErr := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewErr, "marketplace connection error") {
		t.Errorf("expected error in view: %s", viewErr)
	}

	// 3. renderInstall when d.installed == nil falls back to QuickSiteCheck
	write(t, filepath.Join(bench, "sites", "quick.localhost", "site_config.json"), `{"db_name": "_quick", "installed_apps": ["frappe", "erpnext"]}`)
	_ = m.openInstall("quick.localhost")
	m.install.installed = nil // force nil to test renderInstall fallback
	viewFallback := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewFallback, "All local bench apps are installed") {
		t.Errorf("renderInstall failed fallback: %s", viewFallback)
	}

	// 4. openInstall when incomplete state detected directly by QuickSiteCheck
	// A site without db_name is incomplete:
	write(t, filepath.Join(bench, "sites", "incomplete.localhost", "site_config.json"), `{}`)
	_ = m.openInstall("incomplete.localhost")
	if m.install == nil || m.install.unfinished == "" {
		t.Errorf("expected unfinished reason set in openInstall: %+v", m.install)
	}
}

func TestAddCustomDomain(t *testing.T) {
	m, bench := sitesFixture(t)
	m.nav.cursor = slices.Index(m.sites, "good.localhost")

	// Pressing D opens the dialog
	m, cmd := m.Update(press("D"))
	if m.domain == nil || cmd != nil {
		t.Fatal("no domain dialog on D")
	}

	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "Add Custom Domain to good.localhost") {
		t.Errorf("dialog title missing: \n%s", view)
	}

	// Pressing Esc closes the dialog
	m, _ = m.Update(press("esc"))
	if m.domain != nil {
		t.Fatal("domain dialog didn't close on esc")
	}

	// Reopen with 'c'
	m, _ = m.Update(press("c"))
	if m.domain == nil {
		t.Fatal("no domain dialog on c")
	}

	// Submit empty
	m.domain.setFocus(2)
	m, _ = m.Update(press("enter"))
	if m.domain == nil || m.domain.err == "" {
		t.Fatal("expected error on empty submit")
	}

	// Field navigation
	m.domain.setFocus(0)
	m, _ = m.Update(press("tab"))
	if m.domain.focus != 1 {
		t.Errorf("expected focus 1, got %d", m.domain.focus)
	}
	m, _ = m.Update(press("up"))
	if m.domain.focus != 0 {
		t.Errorf("expected focus 0, got %d", m.domain.focus)
	}

	// Fill and submit
	m.domain.domain.SetValue("custom.example.com")
	m.domain.cert.SetValue("/cert.pem")
	m.domain.key.SetValue("/key.pem")
	m.domain.setFocus(2) // move to last field
	m, cmd = m.Update(press("enter"))

	job, ok := find[RunJobMsg](run(cmd))
	if !ok {
		t.Fatal("no job returned on valid submit")
	}
	if !strings.Contains(job.Title, "custom.example.com to good.localhost") {
		t.Errorf("job title: %s", job.Title)
	}
	args := job.Spec.Steps[0].Cmd.Args
	if !slices.Contains(args, "add-domain") || !slices.Contains(args, "custom.example.com") ||
		!slices.Contains(args, "--ssl-certificate") || !slices.Contains(args, "/cert.pem") {
		t.Errorf("job args: %v", args)
	}
	
	// test sites refresh info bar
	write(t, filepath.Join(bench, "sites", "good.localhost", "site_config.json"), `{"db_name": "_good", "domains": ["custom.example.com"]}`)
	m.reload()
	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "domains: custom.example.com") {
		t.Errorf("info bar didn't show domain: \n%s", view)
	}

	// test already registered
	m, _ = m.Update(press("D"))
	m.domain.domain.SetValue("custom.example.com")
	m.domain.setFocus(2)
	m, _ = m.Update(press("enter"))
	if m.domain == nil || !strings.Contains(m.domain.err, "already registered") {
		t.Fatal("expected already registered error")
	}

	// test key update
	m = typeText(m, "x")
	if m.domain.err != "" {
		t.Error("expected error to be cleared on keypress")
	}

	// test shift+tab navigation
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.domain.focus != 1 {
		t.Errorf("expected focus 1, got %d", m.domain.focus)
	}

	// test invalid domain
	m.domain.domain.SetValue("invalid domain!")
	m.domain.setFocus(2)
	m, _ = m.Update(press("enter"))
	if m.domain == nil || !strings.Contains(m.domain.err, "spaces") {
		t.Fatal("expected invalid domain error")
	}

	// test render with error
	if view := ansi.Strip(m.View(100, 30)); !strings.Contains(view, "spaces") {
		t.Errorf("expected error in view, got \n%s", view)
	}
}

// A site whose creation failed is marked, and the mark must go once the same
// site is created successfully -- otherwise a healthy, working site keeps
// telling the user to drop it.
func TestFailedMarkClearsWhenTheSiteIsRecreated(t *testing.T) {
	m, bench := sitesFixture(t) // the fixture already has half.localhost

	m, _ = m.Update(siteFailedMsg{bench: bench, site: "half.localhost"})
	if m.health["half.localhost"] != "last install failed" {
		t.Fatalf("a failed creation was not marked: %v", m.health)
	}
	m, _ = m.Update(siteCreatedMsg{bench: bench, site: "half.localhost"})
	if got, marked := m.health["half.localhost"]; marked && got == "last install failed" {
		t.Fatalf("the failure mark survived a successful recreate: %v", m.health)
	}
	if m.failed["half.localhost"] {
		t.Fatal("the failed flag is still set")
	}
}

func TestSuccessfulCreateAnnouncesTheSite(t *testing.T) {
	m, _ := sitesFixture(t)
	m = openForm(t, m, "ok.localhost")
	_, msgs := submit(t, m)
	job, ok := find[RunJobMsg](msgs)
	if !ok {
		t.Fatal("no job")
	}
	if _, ok := find[siteCreatedMsg](job.After); !ok {
		t.Error("a successful creation does not clear an earlier failure mark")
	}
}
