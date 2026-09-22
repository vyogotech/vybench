package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui/views"
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

type fixture struct {
	app App
	mgr *core.Manager
	fpm *core.FPMClient
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{"VYBENCH_BENCH", "SNAP", "VYBENCH_ETC", "VYBENCH_LOG"} {
		t.Setenv(k, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/index.json" {
			fmt.Fprint(w, `{"packages":[{"org":"acme","appName":"widgets","description":"Widgets for everyone","latest_version":"2.0.0"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("VYBENCH_FPM_REGISTRY", srv.URL)

	packaged := filepath.Join(tmp, "packaged")
	write(t, filepath.Join(packaged, "apps", "frappe", "frappe", "__init__.py"), `__version__ = "16.34.1"`)
	mgr := &core.Manager{VarDir: filepath.Join(tmp, "var"), ConfigDir: filepath.Join(tmp, "cfg"), PackagedPath: packaged}
	for _, name := range []string{"main", "other"} {
		if err := mgr.CreateBench(core.NewBenchOptions{Name: name, SwitchToNew: name == "main"}); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(mgr.VarDir, "benches", "main", "sites", "a.localhost", "site_config.json"), "{}")

	fpm := core.NewFPMClient()
	app := NewApp(mgr, core.NewSupervisor(), fpm)
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return fixture{app: m.(App), mgr: mgr, fpm: fpm}
}

func (f *fixture) send(msgs ...tea.Msg) {
	for _, msg := range msgs {
		m, _ := f.app.Update(msg)
		f.app = m.(App)
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestLayoutFitsTheTerminal(t *testing.T) {
	f := newFixture(t)
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {160, 50}} {
		f.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, tab := range []string{"1", "2", "3", "4", "5"} {
			f.send(keyMsg(tab))
			lines := strings.Split(f.app.View(), "\n")
			if len(lines) != size[1] {
				t.Errorf("%dx%d tab %s: %d lines", size[0], size[1], tab, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%dx%d tab %s line %d is %d wide", size[0], size[1], tab, i, w)
				}
			}
		}
	}
	f.send(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(f.app.View(), "needs at least") {
		t.Error("tiny terminal not handled")
	}
}

// Regression: only the visible tab received messages, so a catalog that
// arrived while the Overview was showing was lost and the marketplace stayed
// empty.
func TestCatalogReachesMarketplaceFromAnotherTab(t *testing.T) {
	f := newFixture(t)
	if f.app.tab != TabOverview {
		t.Fatal("expected to start on the overview")
	}
	mm := views.NewMarketplaceModel(f.fpm, f.app.bench)
	f.send(run(mm.Init())...)
	f.send(keyMsg("3"))
	if view := ansi.Strip(f.app.View()); !strings.Contains(view, "acme/widgets") {
		t.Errorf("marketplace empty after switching tabs:\n%s", view)
	}
}

func TestSwitchingBenchUpdatesHeaderAndPinsSession(t *testing.T) {
	f := newFixture(t)
	if !strings.Contains(f.app.View(), "Bench: main") {
		t.Fatal("header does not show the active bench")
	}
	f.send(keyMsg("b"), keyMsg("down"))
	m, cmd := f.app.Update(keyMsg("enter"))
	f.app = m.(App)
	f.send(run(cmd)...)
	if !strings.Contains(f.app.View(), "Bench: other") {
		t.Errorf("header after switch:\n%s", strings.SplitN(f.app.View(), "\n", 2)[0])
	}
	if got := os.Getenv("VYBENCH_BENCH"); got != filepath.Join(f.mgr.VarDir, "benches", "other") {
		t.Errorf("VYBENCH_BENCH = %q", got)
	}
	if !strings.Contains(ansi.Strip(f.app.View()), "Switched to other") {
		t.Error("no status after switching")
	}
}

func TestDialogsCaptureGlobalKeys(t *testing.T) {
	f := newFixture(t)
	f.send(keyMsg("2"), keyMsg("n"))
	m, cmd := f.app.Update(keyMsg("q"))
	f.app = m.(App)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q quit while typing in the new-site form")
		}
	}
	if f.app.tab != TabSites {
		t.Error("tab changed while typing")
	}
}

func TestJobOverlayBlocksTabsUntilClosed(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.send(views.RunJobMsg{Title: "slow", Spec: core.JobSpec{Steps: []core.Step{{Fn: func(func(string)) error {
		<-release
		return nil
	}}}}})
	f.send(keyMsg("3"))
	if f.app.tab != TabOverview {
		t.Error("tab keys reached the page behind a running job")
	}
	if !strings.Contains(ansi.Strip(f.app.View()), "slow") {
		t.Error("job overlay not drawn")
	}
	m, _ := f.app.Update(views.RunJobMsg{Title: "second"})
	if !f.app.job.Running() || m.(App).job.Running() != true {
		t.Error("a second job replaced the running one")
	}
	close(release)
	f.app.Shutdown()
}

func TestCtrlCDuringJobCancelsInsteadOfQuitting(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.send(views.RunJobMsg{Title: "slow", Spec: core.JobSpec{Steps: []core.Step{{Fn: func(func(string)) error {
		<-release
		return nil
	}}}}})
	m, cmd := f.app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	f.app = m.(App)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, ok := msg.(tea.QuitMsg); ok {
				t.Fatal("ctrl+c quit the app while a job was running")
			}
		}
	}
	if !f.app.job.Active() {
		t.Fatal("ctrl+c dismissed the job overlay")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && f.app.job.Err() == nil {
		time.Sleep(20 * time.Millisecond)
	}
	if f.app.job.Err() != core.ErrCancelled {
		t.Fatalf("ctrl+c did not cancel the job: %v", f.app.job.Err())
	}
}

func TestAppExtended(t *testing.T) {
	f := newFixture(t)

	// Init
	initCmd := f.app.Init()
	if initCmd == nil {
		t.Error("expected non-nil Init command")
	}

	// Width 0 returns Loading...
	zeroApp := f.app
	zeroApp.width = 0
	if zeroApp.View() != "Loading…" {
		t.Errorf("expected Loading…, got %s", zeroApp.View())
	}

	// Tab and shift+tab navigation
	f.send(keyMsg("tab"))
	if f.app.tab != TabSites {
		t.Errorf("expected TabSites, got %d", f.app.tab)
	}
	f.send(keyMsg("shift+tab"))
	if f.app.tab != TabOverview {
		t.Errorf("expected TabOverview, got %d", f.app.tab)
	}

	// Mouse message routing
	f.send(tea.MouseMsg{Type: tea.MouseMotion})

	// Ctrl+c key handling
	_, cmd := f.app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit command on ctrl+c")
	}

	// Status messages: normal, error, long
	f.send(views.StatusMsg{Text: "Everything OK", Err: false})
	if !strings.Contains(ansi.Strip(f.app.View()), "Everything OK") {
		t.Error("expected status text in view")
	}

	f.send(views.StatusMsg{Text: "Error happened", Err: true})
	if !strings.Contains(ansi.Strip(f.app.View()), "Error happened") {
		t.Error("expected error text in view")
	}

	f.send(views.StatusMsg{Text: strings.Repeat("Very long error status that exceeds the available width of status bar ", 5), Err: true})
	_ = f.app.View()

	// Clear status msg
	f.send(clearStatusMsg{seq: f.app.statusSeq})
	if f.app.status != "" {
		t.Errorf("status should be cleared, got %q", f.app.status)
	}
	// Mismatched seq should not clear
	f.app.status = "keep me"
	f.send(clearStatusMsg{seq: 99999})
	if f.app.status != "keep me" {
		t.Error("status should not clear with mismatched seq")
	}

	// Header badges for unregistered and pinned benches
	unregisteredApp := f.app
	unregisteredApp.bench = core.ActiveBench{
		Name:       "unreg",
		Registered: false,
		Source:     core.SourceEnv,
		Entry:      core.BenchEntry{DBEngine: "postgres", FrappeVersion: "v15.0.0"},
	}
	viewUnreg := ansi.Strip(unregisteredApp.View())
	if !strings.Contains(viewUnreg, "unregistered") {
		t.Errorf("expected unregistered in header, got: %s", viewUnreg)
	}
}

func TestSwitchToMarketplaceFromSites(t *testing.T) {
	f := newFixture(t)
	f.send(keyMsg("2")) // switch to Sites tab
	if f.app.tab != TabSites {
		t.Fatalf("expected to be on Sites tab, got %d", f.app.tab)
	}
	m, cmd := f.app.Update(keyMsg("a"))
	f.app = m.(App)
	if cmd == nil {
		t.Fatal("expected command on pressing 'a'")
	}
	msgs := run(cmd)
	for _, msg := range msgs {
		m, cmd2 := f.app.Update(msg)
		f.app = m.(App)
		for _, msg2 := range run(cmd2) {
			m, _ = f.app.Update(msg2)
			f.app = m.(App)
		}
	}

	if f.app.tab != TabMarketplace {
		t.Fatalf("expected to switch to TabMarketplace (2), got %d", f.app.tab)
	}
	if !strings.Contains(f.app.status, "Select an app to install on 'a.localhost'") {
		t.Errorf("status bar missing target site prompt: %q", f.app.status)
	}
}
