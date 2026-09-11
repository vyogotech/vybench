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
