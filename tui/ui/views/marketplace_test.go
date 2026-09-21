package views

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/vyogotech/vybench/tui/core"
)

func TestMarketplaceExtended(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/index.json" {
			fmt.Fprint(w, `{"packages":[
				{"org":"frappe","appName":"crm","description":"CRM app","latest_version":"1.83.0"},
				{"org":"frappe","appName":"hrms","description":"HRMS app","latest_version":"16.0.0"},
				{"org":"frappe","appName":"wiki","description":"Wiki app","latest_version":"2.0.0"}
			]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".json") {
			fmt.Fprint(w, `{"title":"Sample","license":"GPL","description":"Extended info"}`)
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

	bench := t.TempDir()
	write(t, filepath.Join(bench, "sites", "site1.localhost", "site_config.json"), "{}")
	write(t, filepath.Join(bench, "sites", "site2.localhost", "site_config.json"), "{}")

	m := NewMarketplaceModel(core.NewFPMClient(), core.ActiveBench{Name: "b", Path: bench})
	m.SetSize(120, 30)

	for _, msg := range run(m.Init()) {
		m, _ = m.Update(msg)
	}

	// Navigation: down and up
	m, _ = m.Update(press("down"))
	m, _ = m.Update(press("up"))

	// Switch category with left, right, h, l
	m, _ = m.Update(press("right"))
	m, _ = m.Update(press("l"))
	m, _ = m.Update(press("left"))
	m, _ = m.Update(press("h"))

	// Switch target site with 't'
	m, _ = m.Update(press("t"))
	m, _ = m.Update(press("t"))

	// Search filter with '/' and cancel with esc
	m, _ = m.Update(press("/"))
	m = typeText(m, "crm")
	m, _ = m.Update(press("esc"))

	// Search filter with '/' and submit with enter
	m, _ = m.Update(press("/"))
	if !m.CapturingInput() {
		t.Error("expected CapturingInput to be true while searching")
	}
	m = typeText(m, "crm")
	m, _ = m.Update(press("enter"))

	// View rendering
	view := ansi.Strip(m.View(120, 30))
	if !strings.Contains(view, "App Inspector") {
		t.Errorf("expected App Inspector in view, got: %s", view)
	}

	// Bench switched event & AppsChangedMsg
	m, _ = m.Update(BenchSwitchedMsg{Bench: core.ActiveBench{Name: "b", Path: bench}})
	m, _ = m.Update(SitesChangedMsg{})
	m, _ = m.Update(AppsChangedMsg{})

	// compatible helper
	if !compatible([]string{"16", "15"}, "16.34.1") {
		t.Error("compatible should be true for 16")
	}
	if compatible([]string{"15"}, "16.34.1") {
		t.Error("compatible should be false for 15")
	}
	if !compatible(nil, "") {
		t.Error("compatible should be true when version is empty")
	}

	// pillBar branches
	if pb := pillBar(nil, "", 20); pb != "" {
		t.Errorf("expected empty pillBar for nil cats, got %s", pb)
	}
	pbMiddle := pillBar([]string{"cat1", "cat2", "cat3", "cat4", "cat5"}, "cat3", 15)
	if !strings.Contains(pbMiddle, "‹") || !strings.Contains(pbMiddle, "›") {
		t.Errorf("expected scroll indicators in pillBar: %s", pbMiddle)
	}

	// install into bench only (targetSite == "")
	mNoSite := m
	mNoSite.target = len(mNoSite.sites) // target beyond sites list means bench only
	cmdBench := mNoSite.install()
	if cmdBench == nil {
		t.Error("expected cmdBench on install into bench")
	}

	// install when no package selected
	mNoPkg := m
	mNoPkg.filtered = nil
	if cmd := mNoPkg.install(); cmd != nil {
		t.Error("expected nil cmd when no package selected")
	}
}

func TestMarketplaceSetTargetSite(t *testing.T) {
	bench := t.TempDir()
	write(t, filepath.Join(bench, "sites", "s1.localhost", "site_config.json"), "{}")
	write(t, filepath.Join(bench, "sites", "s2.localhost", "site_config.json"), "{}")

	m := NewMarketplaceModel(core.NewFPMClient(), core.ActiveBench{Name: "b", Path: bench})
	if m.targetSite() != "" {
		t.Fatalf("expected initial target site empty, got %q", m.targetSite())
	}
	m.SetTargetSite("s2.localhost")
	if m.targetSite() != "s2.localhost" {
		t.Fatalf("expected target site 's2.localhost', got %q", m.targetSite())
	}
	// Target site not initially in m.sites:
	write(t, filepath.Join(bench, "sites", "s3.localhost", "site_config.json"), "{}")
	m.SetTargetSite("s3.localhost")
	if m.targetSite() != "s3.localhost" {
		t.Fatalf("expected discovered target site 's3.localhost', got %q", m.targetSite())
	}
	// Target site not on disk at all:
	m.SetTargetSite("s4.localhost")
	if m.targetSite() != "s4.localhost" {
		t.Fatalf("expected appended target site 's4.localhost', got %q", m.targetSite())
	}
}

func TestMarketplaceFPMVersionAndUpdate(t *testing.T) {
	bench := t.TempDir()
	m := NewMarketplaceModel(core.NewFPMClient(), core.ActiveBench{Name: "b", Path: bench})
	m.SetSize(120, 30)
	m.loading = false
	m.catalog = core.Catalog{Packages: []core.FPMPackage{{Name: "crm", Org: "frappe"}}}
	m.applyFilter()

	// 1. Initial fpmVersionMsg
	m, _ = m.Update(fpmVersionMsg{version: "4.2.0", latest: "v4.6.0"})
	if m.fpmVersion != "4.2.0" || m.fpmLatest != "v4.6.0" {
		t.Fatalf("expected 4.2.0 / v4.6.0, got %s / %s", m.fpmVersion, m.fpmLatest)
	}

	view := m.View(120, 30)
	if !strings.Contains(view, "fpm 4.2.0 (v4.6.0 available, [u] update)") {
		t.Errorf("expected update hint in view, got %q", view)
	}

	// 2. Press 'u' to initiate update
	var cmd tea.Cmd
	m, cmd = m.Update(press("u"))
	if cmd == nil || !m.fpmUpdating {
		t.Fatal("expected update cmd and fpmUpdating=true")
	}

	// Pressing 'u' again while updating does nothing
	m, cmd2 := m.Update(press("u"))
	if cmd2 != nil {
		t.Fatal("expected nil cmd when already updating")
	}

	// 3. fpmUpdatedMsg failure
	m, cmdErr := m.Update(fpmUpdatedMsg{err: fmt.Errorf("network timeout")})
	if m.fpmUpdating {
		t.Error("fpmUpdating should be false after msg")
	}
	if cmdErr == nil {
		t.Error("expected status message on failure")
	}

	// 4. fpmUpdatedMsg success
	m, cmdSuccess := m.Update(fpmUpdatedMsg{newVersion: "4.6.0"})
	if m.fpmVersion != "4.6.0" {
		t.Errorf("expected version updated to 4.6.0, got %s", m.fpmVersion)
	}
	if cmdSuccess == nil {
		t.Error("expected status message on success")
	}

	// View with matching latest version should not show update prompt
	m, _ = m.Update(fpmVersionMsg{version: "4.6.0", latest: "v4.6.0"})
	viewUpdated := m.View(120, 30)
	if strings.Contains(viewUpdated, "available, [u] update") {
		t.Errorf("unexpected update prompt in updated view: %q", viewUpdated)
	}

	// 5. Test checkFPMVersion command execution
	chkCmd := m.checkFPMVersion()
	if chkCmd != nil {
		msg := chkCmd()
		if _, ok := msg.(fpmVersionMsg); !ok {
			t.Errorf("expected fpmVersionMsg, got %T", msg)
		}
	}

	// 5b. Test checkFPMVersion with Snap env
	t.Setenv("SNAP", "/snap/vybench/current")
	t.Setenv("SNAP_COMMON", t.TempDir())
	chkCmdSnap := m.checkFPMVersion()
	if chkCmdSnap != nil {
		_ = chkCmdSnap()
	}

	// 6. Test updateFPM command execution with tag and without tag
	m.fpmLatest = "v4.6.0"
	updCmd := m.updateFPM()
	if updCmd != nil {
		msg := updCmd()
		if _, ok := msg.(fpmUpdatedMsg); !ok {
			t.Errorf("expected fpmUpdatedMsg, got %T", msg)
		}
	}

	m.fpmLatest = ""
	updCmdEmpty := m.updateFPM()
	if updCmdEmpty != nil {
		_ = updCmdEmpty()
	}
}



