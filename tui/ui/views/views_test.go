package views

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeCLI points VYBENCH_CLI at a stub so bench commands can be built on
// any machine, whatever vybench install it has.
func fakeCLI(t *testing.T) {
	t.Helper()
	cli := filepath.Join(t.TempDir(), "vybench")
	write(t, cli, "#!/bin/sh\n")
	if err := os.Chmod(cli, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VYBENCH_CLI", cli)
}

func testManager(t *testing.T) *core.Manager {
	t.Helper()
	fakeCLI(t)
	tmp := t.TempDir()
	for _, k := range []string{"VYBENCH_BENCH", "SNAP", "VYBENCH_ETC", "VYBENCH_LOG", "VYBENCH_DEFAULT_FRAPPE_VERSION"} {
		t.Setenv(k, "")
	}
	packaged := filepath.Join(tmp, "packaged")
	write(t, filepath.Join(packaged, "apps", "frappe", "frappe", "__init__.py"), `__version__ = "16.34.1"`)
	return &core.Manager{VarDir: filepath.Join(tmp, "var"), ConfigDir: filepath.Join(tmp, "cfg"), PackagedPath: packaged}
}

// run executes cmd and returns the messages it produces, expanding batches.
// Commands that block (timers) are abandoned after a short wait.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, run(c)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(2 * time.Second):
		return nil
	}
}

func find[T any](msgs []tea.Msg) (T, bool) {
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText[M interface{ Update(tea.Msg) (M, tea.Cmd) }](m M, text string) M {
	for _, r := range text {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestPanelIsExactSize(t *testing.T) {
	long := strings.Repeat("wide ", 50) + "\n" + strings.Repeat("row\n", 40)
	for _, sz := range [][2]int{{40, 10}, {12, 3}, {100, 30}} {
		out := panel(sz[0], sz[1], long)
		if w, h := lipgloss.Width(out), lipgloss.Height(out); w != sz[0] || h != sz[1] {
			t.Errorf("panel(%d,%d) is %dx%d", sz[0], sz[1], w, h)
		}
	}
}

func TestOverlayKeepsBaseAroundModal(t *testing.T) {
	base := strings.TrimSuffix(strings.Repeat(strings.Repeat("#", 40)+"\n", 10), "\n")
	out := overlay(base, "┌──┐\n│hi│\n└──┘", 40, 10)
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("%d lines", len(lines))
	}
	mid := ansi.Strip(lines[4])
	if ansi.StringWidth(mid) != 40 || !strings.Contains(mid, "│hi│") || !strings.HasPrefix(mid, "#") || !strings.HasSuffix(mid, "#") {
		t.Errorf("row under the modal: %q", mid)
	}
	if ansi.Strip(lines[0]) != strings.Repeat("#", 40) {
		t.Error("rows outside the modal changed")
	}
}

func TestListNavKeepsCursorVisible(t *testing.T) {
	var n listNav
	for i := 0; i < 25; i++ {
		n.key("down", 30, 10)
	}
	start, end := n.window(30, 10)
	if n.cursor != 25 || n.cursor < start || n.cursor >= end || end-start != 10 {
		t.Errorf("cursor %d window [%d,%d)", n.cursor, start, end)
	}
	n.key("G", 30, 10)
	if n.cursor != 29 {
		t.Errorf("end: %d", n.cursor)
	}
	n.fix(3, 10) // list shrank
	if n.cursor != 2 || n.offset != 0 {
		t.Errorf("after shrink: %+v", n)
	}
}

// Regression: the app read SwitchRequested() twice, so the switch message
// carried an empty bench name.
func TestBenchesEnterSwitchesToSelectedBench(t *testing.T) {
	mgr := testManager(t)
	for _, name := range []string{"alpha", "beta"} {
		if err := mgr.CreateBench(core.NewBenchOptions{Name: name, SwitchToNew: name == "alpha"}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewBenchesModel(mgr, mgr.ResolveActive())
	m.SetSize(100, 30)
	m, _ = m.Update(press("down"))
	m, cmd := m.Update(press("enter"))
	sw, ok := find[BenchSwitchedMsg](run(cmd))
	if !ok {
		t.Fatal("no BenchSwitchedMsg")
	}
	if sw.Bench.Name != "beta" || sw.Bench.Path != filepath.Join(mgr.VarDir, "benches", "beta") {
		t.Errorf("switched to %+v", sw.Bench)
	}
	if mgr.ActiveBenchName() != "beta" {
		t.Error("registry not switched")
	}
}

func TestBenchesNewFormLinksPackagedRelease(t *testing.T) {
	mgr := testManager(t)
	m := NewBenchesModel(mgr, mgr.ResolveActive())
	m.SetSize(100, 30)
	m, _ = m.Update(press("n"))
	if !strings.Contains(ansi.Strip(m.View(100, 30)), "Links the packaged Frappe 16.34.1") {
		t.Error("form does not say the default version is linked instantly")
	}
	m = typeText(m, "fresh")
	m, _ = m.Update(press("enter")) // → engine
	m, _ = m.Update(press("enter")) // → version (prefilled "16")
	m, cmd := m.Update(press("enter"))
	if _, ok := find[BenchesChangedMsg](run(cmd)); !ok {
		t.Fatal("no BenchesChangedMsg")
	}
	m, _ = m.Update(BenchesChangedMsg{}) // the root model broadcasts it back
	if len(m.benches) != 1 || m.benches[0].Name != "fresh" {
		t.Errorf("benches = %+v", m.benches)
	}
}

func TestBenchesNewFormBuildsOtherVersionsWithInit(t *testing.T) {
	mgr := testManager(t)
	m := NewBenchesModel(mgr, mgr.ResolveActive())
	m.SetSize(100, 30)
	m, _ = m.Update(press("n"))
	m = typeText(m, "old")
	m, _ = m.Update(press("tab"))
	m, _ = m.Update(press("tab"))
	m.form.version.SetValue("15")
	if !strings.Contains(ansi.Strip(m.View(100, 30)), "Builds Frappe version-15 with bench init") {
		t.Error("form does not warn that version 15 is built with bench init")
	}
	_, cmd := m.Update(press("enter"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok || !strings.Contains(job.Title, "old") || len(job.Spec.Steps) != 3 {
		t.Fatalf("job = %+v", job)
	}
}

func TestMarketplaceInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/index.json" {
			fmt.Fprint(w, `{"packages":[{"org":"frappe","appName":"crm","latest_version":"1.83.0"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("VYBENCH_FPM_REGISTRY", srv.URL)
	fakeFPM := filepath.Join(t.TempDir(), "fpm")
	write(t, fakeFPM, "#!/bin/sh\n")
	_ = os.Chmod(fakeFPM, 0o755)
	t.Setenv("VYBENCH_FPM", fakeFPM)
	t.Setenv("HOME", t.TempDir())

	bench := t.TempDir()
	write(t, filepath.Join(bench, "sites", "one.localhost", "site_config.json"), "{}")
	m := NewMarketplaceModel(core.NewFPMClient(), core.ActiveBench{Name: "b", Path: bench})
	m.SetSize(120, 30)
	for _, msg := range run(m.Init()) {
		m, _ = m.Update(msg)
	}
	if len(m.filtered) != 1 || m.catalog.Offline {
		t.Fatalf("catalog not loaded: %+v", m.catalog)
	}
	m, _ = m.Update(press("t")) // target the site
	_, cmd := m.Update(press("i"))
	job, ok := find[RunJobMsg](run(cmd))
	if !ok {
		t.Fatal("no install job")
	}
	install := job.Spec.Steps[len(job.Spec.Steps)-1].Cmd.Args
	if !slices.Equal(install[1:], []string{"install", "frappe/crm==1.83.0", "--bench-path", bench, "--site", "one.localhost"}) {
		t.Errorf("install = %v", install)
	}
}

func TestJobOverlayLifecycle(t *testing.T) {
	spec := core.JobSpec{Steps: []core.Step{{Label: "work", Fn: func(log func(string)) error {
		for i := 0; i < 3; i++ {
			log(fmt.Sprintf("line %d", i))
		}
		return nil
	}}}}
	m, cmd := NewJob(RunJobMsg{Title: "Test job", Spec: spec, Success: "all good", After: []tea.Msg{SitesChangedMsg{}}})
	m.SetSize(100, 30)
	if !m.Running() {
		t.Fatal("job not running")
	}
	var after []tea.Msg
	for i := 0; i < 20 && m.Running(); i++ {
		var next []tea.Cmd
		for _, msg := range run(cmd) {
			var c tea.Cmd
			m, c = m.Update(msg)
			if _, ok := msg.(jobDoneMsg); ok {
				after = run(c)
			} else {
				next = append(next, c)
			}
		}
		cmd = tea.Batch(next...)
	}
	if m.Running() {
		t.Fatal("job never finished")
	}
	if st, _ := find[StatusMsg](after); st.Text != "all good" {
		t.Errorf("status = %+v", st)
	}
	if _, ok := find[SitesChangedMsg](after); !ok {
		t.Error("After messages not emitted")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Test job", "done", "line 2", "Esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	m, _ = m.Update(press("esc"))
	if m.Active() {
		t.Error("Esc did not close a finished job")
	}
}

func TestLogsTailAndFilter(t *testing.T) {
	bench := t.TempDir()
	logFile := filepath.Join(bench, "logs", "web.log")
	write(t, logFile, "first line\nERROR something broke\n")
	write(t, filepath.Join(bench, "logs", "worker.log"), "")
	t.Setenv("VYBENCH_LOG", "")

	m := NewLogsModel(core.ActiveBench{Name: "b", Path: bench})
	m.SetSize(100, 30)
	if len(m.files) != 2 || m.files[0].name != "web.log" {
		t.Fatalf("files = %+v", m.files)
	}
	for _, msg := range run(m.SetVisible(true)) {
		m, _ = m.Update(msg)
	}
	if !strings.Contains(ansi.Strip(m.View(100, 30)), "ERROR something broke") {
		t.Fatal("existing lines not shown")
	}

	f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintln(f, "appended later")
	_ = f.Close()
	for _, msg := range run(m.read()) {
		m, _ = m.Update(msg)
	}
	if !strings.Contains(ansi.Strip(m.View(100, 30)), "appended later") {
		t.Error("appended line not tailed")
	}

	m, _ = m.Update(press("/"))
	m = typeText(m, "error")
	view := ansi.Strip(m.View(100, 30))
	if !strings.Contains(view, "ERROR something broke") || strings.Contains(view, "first line") {
		t.Error("filter not applied")
	}
	m, _ = m.Update(press("esc"))
	if m.CapturingInput() || m.filter.Value() != "" {
		t.Error("Esc did not clear the filter")
	}

	// Switching files starts a new generation; stale chunks are ignored.
	oldGen := m.gen
	m, _ = m.Update(press("l"))
	if m.gen == oldGen || m.active != 1 {
		t.Errorf("file switch: gen %d active %d", m.gen, m.active)
	}
	m, _ = m.Update(logChunkMsg{gen: oldGen, lines: []string{"stale"}})
	if slices.Contains(m.lines, "stale") {
		t.Error("stale chunk applied")
	}
}

// Every view must render exactly w×h on its own; the root model's final clip
// would otherwise hide an overflow by cutting off panel borders.
func TestViewsRenderExactlyTheirSize(t *testing.T) {
	mgr := testManager(t)
	if err := mgr.CreateBench(core.NewBenchOptions{Name: "main", SwitchToNew: true}); err != nil {
		t.Fatal(err)
	}
	bench := mgr.ResolveActive()
	write(t, filepath.Join(bench.Path, "sites", "a-very-long-site-name-for-layout.localhost", "site_config.json"), "{}")
	write(t, filepath.Join(bench.Path, "logs", "web.log"), strings.Repeat("a long log line that is wider than any terminal ", 10)+"\n")

	market := NewMarketplaceModel(core.NewFPMClient(), bench)
	market, _ = market.Update(catalogMsg{cat: core.Catalog{Packages: []core.FPMPackage{
		{Org: "frappe", Name: "builder", Version: "0.0.0-git.20260903.cd3438d1ab", Category: "Web", Description: strings.Repeat("words ", 40)},
	}}})
	market.details["frappe/builder"] = &detailState{details: core.PackageDetails{
		License: "MIT", WheelPlatform: "manylinux2014_s390x", SourceURL: "https://github.com/frappe/builder",
		Dependencies: []string{"frappe/frappe >=16", "frappe/erpnext >=16", "frappe/payments >=0"},
	}}
	logs := NewLogsModel(bench)
	sites := NewSitesModel(mgr, nil, bench)
	benches := NewBenchesModel(mgr, bench)
	overview := NewOverviewModel(core.NewSupervisor(), mgr, bench)
	overview, _ = overview.Update(servicesMsg{services: []core.Service{{Name: "vybench", Label: "Frappe (vybench-with-a-long-name)", Detail: "not installed"}}})

	for _, size := range [][2]int{{60, 13}, {80, 21}, {100, 27}, {160, 47}} {
		w, h := size[0], size[1]
		market.SetSize(w, h)
		logs.SetSize(w, h)
		sites.SetSize(w, h)
		benches.SetSize(w, h)
		overview.SetSize(w, h)
		for name, view := range map[string]string{
			"overview": overview.View(w, h), "sites": sites.View(w, h), "marketplace": market.View(w, h),
			"logs": logs.View(w, h), "benches": benches.View(w, h),
		} {
			if got := lipgloss.Width(view); got > w {
				t.Errorf("%s at %dx%d is %d wide", name, w, h, got)
			}
			if got := lipgloss.Height(view); got != h {
				t.Errorf("%s at %dx%d is %d tall", name, w, h, got)
			}
		}
	}
}

func TestDialogsFitA24RowTerminal(t *testing.T) {
	mgr := testManager(t)
	for _, name := range []string{"main", "other"} {
		if err := mgr.CreateBench(core.NewBenchOptions{Name: name, SwitchToNew: name == "main"}); err != nil {
			t.Fatal(err)
		}
	}
	const w, h = 80, 21
	sites := NewSitesModel(mgr, nil, mgr.ResolveActive())
	sites.SetSize(w, h)
	sites, _ = sites.Update(press("n"))
	sites.form.engine = "postgres"
	sites.form.exists = true
	sites.form.db, sites.form.dbWhy = dbDown, "another MariaDB server holds port 3306"
	sites.form.check = &core.SiteCheck{State: core.SiteUnknown, Reason: "MariaDB is not running"}
	sites.form.apps, sites.form.appOn = []string{"erpnext", "hrms", "crm"}, map[string]bool{"erpnext": true}
	sites.form.setFocus(fieldApps)
	sites.form.err = strings.Repeat("a validation error ", 4)

	benches := NewBenchesModel(mgr, mgr.ResolveActive())
	benches.SetSize(w, h)
	benches, _ = benches.Update(press("n"))
	benches.form.engine = "postgres"
	benches.form.version.SetValue("15")
	benches.form.err = strings.Repeat("a long validation error ", 5)

	for name, d := range map[string]string{"new site": sites.renderForm(), "new bench": benches.viewForm()} {
		if got := lipgloss.Height(d); got > h {
			t.Errorf("%s dialog is %d rows, the screen has %d", name, got, h)
		}
		if got := lipgloss.Width(d); got > w {
			t.Errorf("%s dialog is %d wide", name, got)
		}
	}
}

func TestOverlayKeepsTheBottomOfATallDialog(t *testing.T) {
	var rows []string
	for i := 0; i < 30; i++ {
		rows = append(rows, fmt.Sprintf("row %d", i))
	}
	d := dialog("Tall", rows, "KEYS")
	out := overlay(strings.Repeat(" \n", 9)+" ", d, 40, 10)
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) != 10 || !strings.Contains(lines[8], "KEYS") || !strings.Contains(lines[9], "╰") {
		t.Errorf("bottom of the dialog lost:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOverviewShowsWhatTheBenchNeeds(t *testing.T) {
	m := NewOverviewModel(nil, nil, core.ActiveBench{Name: "test", Path: "/benches/test"})
	m.SetSize(100, 24)
	m, _ = m.Update(servicesMsg{
		services: []core.Service{{Name: "vybench-mariadb", Label: "MariaDB (port 13306)", State: core.StateStopped}},
		health:   core.BenchHealth{DBUp: false, DBWhy: "another program holds port 13306", WebPort: 8000, WebUp: true, Serving: "/benches/default"},
		serving:  "default",
	})
	view := ansi.Strip(m.View(100, 24))
	for _, want := range []string{"MariaDB (port 13306)", "MariaDB is not reachable", "another program holds port", "serving bench 'default'"} {
		if !strings.Contains(view, want) {
			t.Errorf("overview missing %q:\n%s", want, view)
		}
	}
}
