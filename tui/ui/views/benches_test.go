package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestBenchesAttachAndDrop(t *testing.T) {
	mgr := testManager(t)
	bench1 := filepath.Join(mgr.VarDir, "benches", "one")
	bench2 := filepath.Join(mgr.VarDir, "benches", "two")
	_ = mgr.CreateBench(core.NewBenchOptions{Name: "one", SwitchToNew: true})
	_ = mgr.CreateBench(core.NewBenchOptions{Name: "two", SwitchToNew: false})

	m := NewBenchesModel(mgr, mgr.ResolveActive())
	m.SetSize(120, 30)

	// CapturingInput when no dialog is open
	if m.CapturingInput() {
		t.Error("CapturingInput should be false initially")
	}

	// Open attach form
	m, _ = m.Update(press("a"))
	if m.attach == nil || !m.CapturingInput() {
		t.Fatal("attach form not open")
	}
	viewAttach := ansi.Strip(m.View(120, 30))
	if !strings.Contains(viewAttach, "Attach Existing Bench") {
		t.Errorf("expected Attach Existing Bench in view, got: %s", viewAttach)
	}

	// Tab through fields in attach form
	m, _ = m.Update(press("tab"))
	m, _ = m.Update(press("up"))
	m, _ = m.Update(press("down"))

	// Esc to cancel attach form
	m, _ = m.Update(press("esc"))
	if m.attach != nil {
		t.Fatal("attach form should be closed on esc")
	}

	// Reopen attach and submit invalid path
	m, _ = m.Update(press("a"))
	m = typeText(m, "attached-bench")
	m, _ = m.Update(press("enter")) // focus moves to path
	m = typeText(m, "/nonexistent/path/for/bench")
	m, _ = m.Update(press("enter"))
	if m.attach == nil || m.attach.err == "" {
		t.Error("expected attach error for non-existent path")
	}

	// Attach valid bench directory
	validDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(validDir, "sites"), 0755)
	_ = os.WriteFile(filepath.Join(validDir, "sites", "common_site_config.json"), []byte("{}"), 0644)
	m.attach.path.SetValue(validDir)
	m, cmd := m.Update(press("enter"))
	if m.attach != nil {
		t.Errorf("attach form should close on success: err=%s", m.attach.err)
	}
	_ = run(cmd)

	// Test Drop bench
	m, _ = m.Update(press("d"))
	if m.dropping == "" {
		t.Fatal("expected dropping to be set on 'd'")
	}
	viewDrop := ansi.Strip(m.View(120, 30))
	if !strings.Contains(viewDrop, "Remove") {
		t.Errorf("expected drop prompt in view: %s", viewDrop)
	}

	// Cancel drop with 'n'
	m, _ = m.Update(press("n"))
	if m.dropping != "" {
		t.Fatal("dropping should be cleared on 'n'")
	}

	// Confirm drop with 'y'
	m, _ = m.Update(press("d"))
	m, cmd = m.Update(press("y"))
	if m.dropping != "" {
		t.Fatal("dropping should be cleared after confirmation")
	}
	_ = run(cmd)

	// Reload with 'r'
	m, _ = m.Update(press("r"))

	// New bench form flow
	m, _ = m.Update(press("n"))
	if m.form == nil || !m.CapturingInput() {
		t.Fatal("expected new bench form to open")
	}
	// Focus 0: name
	m, _ = m.Update(press("tab")) // focus 1: engine
	m, _ = m.Update(press("right"))
	m, _ = m.Update(press("left"))
	m, _ = m.Update(press("p"))
	m, _ = m.Update(press("m"))
	m, _ = m.Update(press(" "))
	m, _ = m.Update(press("tab")) // focus 2: version
	m, _ = m.Update(press("up"))  // back to focus 1
	m, _ = m.Update(press("down"))
	// Cancel with esc
	m, _ = m.Update(press("esc"))
	if m.form != nil {
		t.Error("expected form to close on esc")
	}

	// Reopen and submit with invalid name
	m, _ = m.Update(press("n"))
	m.form.name.SetValue("bad bench name!")
	m.form.setFocus(2)
	m, _ = m.Update(press("enter"))
	if m.form == nil || m.form.err == "" {
		t.Error("expected validation error for invalid bench name")
	}

	// switchTo edge cases: missing bench, already active bench
	cmdMissing := m.switchTo(core.BenchInfo{Name: "missing", Missing: true})
	if cmdMissing == nil {
		t.Error("expected error cmd for missing bench")
	}
	cmdActive := m.switchTo(core.BenchInfo{Name: "one", IsActive: true})
	if cmdActive == nil {
		t.Error("expected status cmd for already active bench")
	}

	_ = bench1
	_ = bench2
}

func TestBenchesPlanPreviewAndAttach(t *testing.T) {
	mgr := testManager(t)
	bench := filepath.Join(mgr.VarDir, "benches", "main")
	_ = mgr.CreateBench(core.NewBenchOptions{Name: "main", SwitchToNew: true})

	m := NewBenchesModel(mgr, mgr.ResolveActive())

	// planPreview with packaged version
	p1 := m.planPreview("")
	if len(p1) == 0 || !strings.Contains(p1[0], "Links the packaged Frappe") {
		t.Errorf("expected packaged link preview, got: %v", p1)
	}

	// planPreview with unpackaged
	m.packaged = ""
	p2 := m.planPreview("")
	if len(p2) == 0 || !strings.Contains(p2[0], "Enter a Frappe version") {
		t.Errorf("expected enter version preview, got: %v", p2)
	}
	p3 := m.planPreview("15")
	if len(p3) < 2 || !strings.Contains(p3[0], "No packaged Frappe") {
		t.Errorf("expected no packaged Frappe preview, got: %v", p3)
	}

	// newAttachForm when current is not registered
	unreg := m
	unreg.current.Registered = false
	unreg.current.Path = bench
	form := unreg.newAttachForm()
	if form.path.Value() != bench {
		t.Errorf("expected attach path to prefill current path, got %s", form.path.Value())
	}

	// Message updates
	m, _ = m.Update(BenchesChangedMsg{})
	m, _ = m.Update(SitesChangedMsg{})

	// dbCheckMsg while newForm is open
	m, _ = m.Update(press("n"))
	m, _ = m.Update(dbCheckMsg{bench: "", engine: "mariadb", up: true})
	m, _ = m.Update(press("esc"))

	// Switch via space key
	m.nav.cursor = 0
	m, cmdSpace := m.Update(press(" "))
	if cmdSpace == nil {
		t.Error("expected cmdSpace on ' '")
	}

	// Cursor beyond length
	m.nav.cursor = len(m.benches) + 10
	m, _ = m.Update(press("enter"))
	m, _ = m.Update(press("d"))
}



