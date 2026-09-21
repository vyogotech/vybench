package views

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestSitesDropRestoreAndForms(t *testing.T) {
	m, bench := sitesFixture(t)

	// Verify CapturingInput false when no modal open
	if m.CapturingInput() {
		t.Error("CapturingInput should be false when on list")
	}

	// 1. Drop form flow
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, _ = m.Update(press("d"))
	if m.drop == nil {
		t.Fatal("drop form did not open on d")
	}
	if !m.CapturingInput() {
		t.Error("CapturingInput should be true when drop form open")
	}
	viewDrop := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewDrop, "Drop site good.localhost") {
		t.Errorf("drop view unexpected: %s", viewDrop)
	}

	// Drop form key navigation
	m, _ = m.Update(press("tab"))
	m, _ = m.Update(press("up"))
	m, _ = m.Update(press("down"))
	m, _ = m.Update(press(" ")) // toggle checkbox

	// Cancel drop form with Esc
	m, _ = m.Update(press("esc"))
	if m.drop != nil {
		t.Error("drop form should be nil after esc")
	}

	// Reopen drop form and submit
	m, _ = m.Update(press("d"))
	m.drop.input.SetValue("good.localhost")
	m, cmdDrop := m.Update(press("enter"))
	if cmdDrop == nil {
		t.Error("expected cmdDrop on enter")
	}

	// 2. Restore form flow
	m, _ = m.Update(press("R"))
	if m.restore == nil {
		t.Fatal("restore form did not open on R")
	}
	if !m.CapturingInput() {
		t.Error("CapturingInput should be true when restore form open")
	}
	viewRestore := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewRestore, "Restore") {
		t.Errorf("restore view unexpected: %s", viewRestore)
	}

	// Restore form key navigation
	m, _ = m.Update(press("tab"))
	m, _ = m.Update(press("shift+tab"))
	m, _ = m.Update(press("down"))
	m, _ = m.Update(press("up"))

	// Cancel restore with Esc
	m, _ = m.Update(press("esc"))
	if m.restore != nil {
		t.Error("restore form should be nil after esc")
	}

	// Reopen restore and submit with backup
	m, _ = m.Update(press("R"))
	sqlPath := filepath.Join(bench, "test_backup.sql.gz")
	write(t, sqlPath, "dummy sql")
	m.restore.path.SetValue(sqlPath)
	m.restore.target.SetValue("good.localhost")
	m.restore.pick = len(m.restore.backups) // pick other file
	m.restore.db = dbUp
	m.restore.refresh()
	rFields := m.restore.fields()
	m.restore.focus = rFields[len(rFields)-1]
	m, cmdRestore := m.Update(press("enter"))
	if cmdRestore == nil {
		t.Error("expected cmdRestore on enter")
	}
	if m.restore != nil {
		t.Error("expected restore dialog to close after submission")
	}

	// 3. New site form navigation & Esc
	m, _ = m.Update(press("n"))
	if m.form == nil || !m.CapturingInput() {
		t.Fatal("new site form did not open on n")
	}
	viewNew := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewNew, "New Site") {
		t.Errorf("new site view unexpected: %s", viewNew)
	}
	// Tab through all fields in new site form
	for i := 0; i < 8; i++ {
		m, _ = m.Update(press("tab"))
	}
	// Shift-tab backward
	for i := 0; i < 4; i++ {
		m, _ = m.Update(press("shift+tab"))
	}
	// Up and down
	m, _ = m.Update(press("up"))
	m, _ = m.Update(press("down"))

	// Esc to cancel new site form
	m, _ = m.Update(press("esc"))
	if m.form != nil {
		t.Error("new site form should be nil after esc")
	}

	// 4. Site list interactions: openURL, reload, visible
	m, cmdOpen := m.Update(press("o"))
	if cmdOpen == nil {
		t.Error("expected cmdOpen on o")
	}

	m, cmdReload := m.Update(press("r"))
	if cmdReload != nil {
		t.Error("expected nil cmd on r reload")
	}

	// Test B (backup)
	m, cmdBackup := m.Update(press("B"))
	if cmdBackup == nil {
		t.Error("expected cmdBackup on B")
	}

	m.SetVisible(false)
	m.SetVisible(true)

	// Test BenchSwitchedMsg
	m, _ = m.Update(BenchSwitchedMsg{Bench: core.ActiveBench{Name: "b", Path: bench}})
}

func TestSitesDialogBranches(t *testing.T) {
	m, bench := sitesFixture(t)

	// --- Drop Dialog branches ---
	m.nav.cursor = slices.Index(m.sites, "good.localhost")
	m, _ = m.Update(press("d"))
	// Type mismatched name
	m = typeText(m, "wrong.localhost")
	m, _ = m.Update(press("enter"))
	if !strings.Contains(m.drop.err, "does not match") {
		t.Errorf("expected mismatch error, got %s", m.drop.err)
	}

	// Toggle focusForce and toggle force checkbox
	m, _ = m.Update(press("tab"))
	if !m.drop.focusForce {
		t.Error("expected focusForce to be true")
	}
	m, _ = m.Update(press(" "))
	if !m.drop.force {
		t.Error("expected force to be toggled on")
	}
	m, _ = m.Update(press("tab")) // focus back to input

	// Drop with dbDown
	m.drop.db = dbDown
	m.drop.input.SetValue("good.localhost")
	m, _ = m.Update(press("enter"))
	if !strings.Contains(m.drop.err, "not running") {
		t.Errorf("expected not running error, got %s", m.drop.err)
	}
	// ctrl+s in drop dialog when dbDown
	m, cmdDB := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmdDB == nil {
		t.Error("expected DB start cmd on ctrl+s")
	}
	m, _ = m.Update(press("esc")) // close drop

	// --- Install Dialog branches ---
	_ = m.openInstall("good.localhost")
	m.install.apps = []string{"erpnext", "hrms"}
	// Navigation: k, j, h, l
	m, _ = m.Update(press("j"))
	m, _ = m.Update(press("k"))
	m, _ = m.Update(press("l"))
	m, _ = m.Update(press("h"))

	// Enter with unfinished site
	m.install.unfinished = "database missing"
	m, _ = m.Update(press("enter"))
	if !strings.Contains(m.install.err, "did not finish installing") {
		t.Errorf("expected unfinished error, got %s", m.install.err)
	}
	m.install.unfinished = ""

	// Enter with dbDown
	m.install.db = dbDown
	m, _ = m.Update(press("enter"))
	if !strings.Contains(m.install.err, "not running") {
		t.Errorf("expected db down error, got %s", m.install.err)
	}
	// ctrl+s in install dialog
	m, cmdInstDB := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmdInstDB == nil {
		t.Error("expected DB start cmd on ctrl+s in install dialog")
	}

	// Enter with empty apps closes dialog
	m.install.apps = nil
	m, _ = m.Update(press("enter"))
	if m.install != nil {
		t.Error("expected install dialog to close with empty apps")
	}

	// --- Restore Dialog branches ---
	m, _ = m.Update(press("R"))
	// Type text into rTarget
	m.restore.focus = rTarget
	m = typeText(m, "x")
	// Type text into rPath
	m.restore.focus = rPath
	m = typeText(m, "y")

	// ctrl+s in restore dialog when dbDown
	m.restore.db = dbDown
	m, cmdRestDB := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmdRestDB == nil {
		t.Error("expected DB start cmd on ctrl+s in restore dialog")
	}

	// Toggle checkboxes in restore dialog
	m.restore.focus = rFiles
	m, _ = m.Update(press(" "))
	m.restore.focus = rForce
	m, _ = m.Update(press(" "))
	m, _ = m.Update(press("esc")) // close restore

	// --- New Site Form branches ---
	m, _ = m.Update(press("n"))
	// Typing text into domain input
	m.form.focus = fieldDomain
	m = typeText(m, "test")
	// Typing text into adminPW input
	m.form.focus = fieldAdminPW
	m = typeText(m, "secret")

	// Toggle engine with 'p' and 'm' and space
	m.form.focus = fieldEngine
	m, _ = m.Update(press("p"))
	if m.form.engine != "postgres" {
		t.Errorf("expected engine postgres, got %s", m.form.engine)
	}
	m, _ = m.Update(press("m"))
	if m.form.engine != "mariadb" {
		t.Errorf("expected engine mariadb, got %s", m.form.engine)
	}
	m, _ = m.Update(press(" "))

	// Toggle force in new site form
	m.form.focus = fieldForce
	m, _ = m.Update(press(" "))

	// ctrl+s when dbDown
	m.form.db = dbDown
	m, cmdFormDB := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmdFormDB == nil {
		t.Error("expected DB start cmd on ctrl+s in new site form")
	}
	m, _ = m.Update(press("esc")) // close form

	_ = bench
}

