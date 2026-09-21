package views

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestOverviewModel(t *testing.T) {
	mgr := testManager(t)
	bench := filepath.Join(mgr.VarDir, "benches", "main")
	_ = mgr.CreateBench(core.NewBenchOptions{Name: "main", SwitchToNew: true})

	sup := core.NewSupervisor()
	active := mgr.ResolveActive()
	m := NewOverviewModel(sup, mgr, active)
	m.SetSize(120, 30)

	// Init
	initCmd := m.Init()
	if initCmd == nil {
		t.Fatal("Init returned nil")
	}

	// SetVisible
	_ = m.SetVisible(true)

	// Update with servicesMsg
	srvMsg := servicesMsg{
		services: []core.Service{
			{Name: "vybench.mariadb", Label: "MariaDB", State: core.StateRunning},
			{Name: "vybench.web", Label: "Web Server", State: core.StateStopped},
		},
		health: core.BenchHealth{
			DBUp:    true,
			WebUp:   true,
			WebPort: 8000,
			Serving: bench,
		},
		serving: "main",
	}
	m, _ = m.Update(srvMsg)

	view := ansi.Strip(m.View(120, 30))
	if !strings.Contains(view, "Services") || !strings.Contains(view, "MariaDB") {
		t.Errorf("View does not contain expected service text: %s", view)
	}

	// Key actions: s, x, r
	for _, key := range []string{"s", "x", "r"} {
		var cmd tea.Cmd
		m, cmd = m.Update(press(key))
		if cmd == nil {
			t.Errorf("key %s should produce command", key)
		}
	}

	// Tick message
	m.visible = true
	m.loading = false
	m, _ = m.Update(overviewTickMsg{})

	// Events
	m, _ = m.Update(ServicesChangedMsg{})
	m, _ = m.Update(BenchSwitchedMsg{Bench: active})
	m, _ = m.Update(SitesChangedMsg{})

	// Visibility transitions
	_ = m.SetVisible(false)
	m.loading = false
	cmdVis := m.SetVisible(true)
	if cmdVis != nil {
		_ = run(cmdVis)
	}
}

func TestOverviewServiceStates(t *testing.T) {
	mgr := testManager(t)
	active := mgr.ResolveActive()
	m := NewOverviewModel(core.NewSupervisor(), mgr, active)

	// Case 1: Empty services with error
	m.services = nil
	m.svcErr = errors.New("cannot connect to systemd")
	m.loading = false
	s1 := m.renderServices(40)
	if !strings.Contains(s1, "cannot connect") {
		t.Errorf("expected error in renderServices, got: %s", s1)
	}

	// Case 2: No services found (no error, not loading)
	m.svcErr = nil
	s2 := m.renderServices(40)
	if !strings.Contains(s2, "No services found") {
		t.Errorf("expected No services found, got: %s", s2)
	}

	// Case 3: Failed, conflict, unknown states, and detail wrapping
	m.services = []core.Service{
		{Name: "svc.failed", Label: "Failed Svc", State: core.StateFailed, Detail: "exited with code 1"},
		{Name: "svc.conflict", Label: "Conflict Svc", State: core.StateConflict, Detail: "port in use"},
		{Name: "svc.unknown", Label: "Unknown Svc", State: core.ServiceState(99), Detail: "status unknown"},
		{Name: "svc.long", Label: "Long Label Svc", State: core.StateRunning, Detail: "very long detail string that exceeds width 20"},
	}
	s3 := m.renderServices(20)
	if !strings.Contains(s3, "Failed Svc") || !strings.Contains(s3, "Conflict Svc") {
		t.Errorf("expected badges in renderServices, got: %s", s3)
	}
}

func TestOverviewRenderBenchVariants(t *testing.T) {
	mgr := testManager(t)
	bench := filepath.Join(mgr.VarDir, "benches", "main")
	_ = mgr.CreateBench(core.NewBenchOptions{Name: "main", SwitchToNew: true})

	m := NewOverviewModel(core.NewSupervisor(), mgr, core.ActiveBench{
		Name:       "main",
		Path:       bench,
		Source:     core.SourceEnv, // pinned!
		Registered: false,          // not registered!
	})

	// 1. Pinned & Unregistered & 0 sites
	m.sites = nil
	m.health = &core.BenchHealth{
		DBUp:    true,
		WebUp:   true,
		WebPort: 8000,
		Serving: "/different/bench", // different serving bench!
	}
	m.serving = "other"

	bView := m.renderBench(60, 20)
	if !strings.Contains(bView, "not registered") || !strings.Contains(bView, "pinned") || !strings.Contains(bView, "serving bench 'other'") {
		t.Errorf("renderBench unexpected output: %s", bView)
	}

	// 2. Many sites exceeding room
	for i := 0; i < 25; i++ {
		m.sites = append(m.sites, fmt.Sprintf("site%d.localhost", i))
	}
	bViewMany := m.renderBench(60, 22)
	if !strings.Contains(bViewMany, "… and") {
		t.Errorf("expected ellipsis in site list: %s", bViewMany)
	}
}



