package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorePlatformHelpers(t *testing.T) {
	// Platform String
	for p, expected := range map[Platform]string{
		PlatformBrew:    "homebrew",
		PlatformSnap:    "snap",
		PlatformNative:  "native",
		PlatformUnknown: "unknown",
	} {
		if p.String() != expected {
			t.Errorf("platform string for %v: expected %s, got %s", p, expected, p.String())
		}
	}

	// DetectPlatform
	t.Setenv("SNAP", "/snap/vybench/current")
	if DetectPlatform() != PlatformSnap {
		t.Errorf("expected PlatformSnap when SNAP is set, got %v", DetectPlatform())
	}
	t.Setenv("SNAP", "")

	// InstanceName
	t.Setenv("VYBENCH_NAME", "custom-bench-instance")
	if InstanceName() != "custom-bench-instance" {
		t.Errorf("expected custom-bench-instance, got %s", InstanceName())
	}
	t.Setenv("VYBENCH_NAME", "")
	t.Setenv("SNAP_INSTANCE_NAME", "snap-instance")
	if InstanceName() != "snap-instance" {
		t.Errorf("expected snap-instance, got %s", InstanceName())
	}
	t.Setenv("SNAP_INSTANCE_NAME", "")

	// RestartHint
	t.Setenv("SNAP", "/snap/vybench/current")
	_ = RestartHint()
	t.Setenv("SNAP", "")

	// PackagedBenchPath
	t.Setenv("SNAP", "/snap/vybench/current")
	if PackagedBenchPath() != "/snap/vybench/current/opt/frappe-bench" {
		t.Errorf("unexpected PackagedBenchPath: %s", PackagedBenchPath())
	}
	t.Setenv("SNAP", "")

	// OpenURLCommand
	cmd := OpenURLCommand("http://localhost:8000")
	if cmd == nil || len(cmd.Args) == 0 {
		t.Error("OpenURLCommand returned empty command")
	}

	// initPython
	t.Setenv("SNAP", t.TempDir())
	_ = initPython()
	t.Setenv("SNAP", "")
}

func TestCoreBenchManagerExtended(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VYBENCH_ETC", filepath.Join(tmp, "etc"))
	t.Setenv("VYBENCH_VAR", filepath.Join(tmp, "var"))

	mgr := NewManager()
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	// ActiveBenchPath, BenchPaths, NameFor, Bench
	_ = mgr.ActiveBenchPath()
	_ = mgr.BenchPaths()
	_ = mgr.NameFor(mgr.ActiveBenchPath())
	_, _ = mgr.Bench("non-existent")

	// LegacyBenchPath
	_ = mgr.LegacyBenchPath()

	// PinReason
	a1 := ActiveBench{Source: SourceEnv}
	if a1.PinReason() == "" || !a1.Pinned() {
		t.Error("expected PinReason for SourceEnv")
	}
	a2 := ActiveBench{Source: SourceCwd}
	if a2.PinReason() == "" || !a2.Pinned() {
		t.Error("expected PinReason for SourceCwd")
	}
	a3 := ActiveBench{Source: SourceSymlink}
	if a3.PinReason() != "" || a3.Pinned() {
		t.Error("expected empty PinReason for SourceSymlink")
	}

	// MajorVersion
	if v := MajorVersion("16.34.1"); v != "16" {
		t.Errorf("expected 16, got %s", v)
	}
	if v := MajorVersion("v15.2"); v != "15" {
		t.Errorf("expected 15, got %s", v)
	}
	if v := MajorVersion("14"); v != "14" {
		t.Errorf("expected 14, got %s", v)
	}

	// SamePath
	p1 := filepath.Join(tmp, "a", "..", "b")
	p2 := filepath.Join(tmp, "b")
	_ = os.MkdirAll(p2, 0755)
	if !SamePath(p1, p2) {
		t.Errorf("SamePath(%q, %q) returned false", p1, p2)
	}
}

func TestCoreDBHelpers(t *testing.T) {
	if DBLabel("mariadb") != "MariaDB" || DBLabel("postgres") != "PostgreSQL" {
		t.Errorf("unexpected DBLabel")
	}

	// Diagnose
	lines := []string{
		"Error: Can't connect to local server through socket",
		"Access denied for user 'root'@'localhost'",
		"database already exists, use `--force`",
		"No module named 'frappe'",
		"no bench command found",
	}
	if d := Diagnose(lines[:1]); d == "" {
		t.Error("Diagnose should detect connection failure")
	}
	if d := Diagnose(lines[1:2]); d == "" {
		t.Error("Diagnose should detect access denied")
	}
	if d := Diagnose(lines[2:3]); d == "" {
		t.Error("Diagnose should detect already exists")
	}
	if d := Diagnose(lines[3:4]); d == "" {
		t.Error("Diagnose should detect missing module")
	}
	if d := Diagnose(lines[4:5]); d == "" {
		t.Error("Diagnose should detect no bench command")
	}
	if d := Diagnose([]string{"clean normal line"}); d != "" {
		t.Errorf("unexpected diagnosis: %s", d)
	}

	// OwnMariaDB
	_ = OwnMariaDB()

	// configInt
	if configInt(map[string]any{"port": json.Number("3306")}, "port") != 3306 {
		t.Error("configInt failed for json.Number")
	}
	if configInt(map[string]any{"port": "5432"}, "port") != 5432 {
		t.Error("configInt failed for string number")
	}
	if configInt(map[string]any{}, "port") != 0 {
		t.Error("configInt failed for empty")
	}

	// DatabaseStatus
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"db_type": "mariadb", "db_socket": "/nonexistent.sock", "db_port": 65530}`)
	up, _ := DatabaseStatus(bench, "mariadb")
	if up {
		t.Error("DatabaseStatus should be false for unreachable socket")
	}

	// StartDatabaseSteps and WaitForDatabaseStep
	sup := NewSupervisor()
	_ = sup.clearStaleSocket(func(string) {})
	step := WaitForDatabaseStep(bench, "mariadb", 10*time.Millisecond)
	if step.Label == "" || step.Fn == nil {
		t.Error("WaitForDatabaseStep returned empty step")
	}
	_ = step.Fn(func(string) {})
}

func TestCoreSiteOpsExtended(t *testing.T) {
	// humanSize
	if humanSize(500) != "500 B" {
		t.Errorf("expected 500 B, got %s", humanSize(500))
	}
	if humanSize(2048) != "2 KB" {
		t.Errorf("expected 2 KB, got %s", humanSize(2048))
	}
	if humanSize(2*1024*1024) != "2.0 MB" {
		t.Errorf("expected 2.0 MB, got %s", humanSize(2*1024*1024))
	}
	if humanSize(3*1024*1024*1024) != "3.0 GB" {
		t.Errorf("expected 3.0 GB, got %s", humanSize(3*1024*1024*1024))
	}

	// ExpandHome
	if p, err := ExpandHome("~/test"); err != nil || p == "~/test" {
		t.Errorf("ExpandHome failed: %v %s", err, p)
	}
	if p, err := ExpandHome("/abs/path"); err != nil || p != "/abs/path" {
		t.Errorf("ExpandHome changed abs path: %s", p)
	}

	// archiveSite
	bench := t.TempDir()
	siteDir := filepath.Join(bench, "sites", "old.localhost")
	_ = os.MkdirAll(siteDir, 0755)
	dest, err := archiveSite(bench, "old.localhost")
	if err != nil {
		t.Fatalf("archiveSite failed: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("archive dest does not exist: %s", dest)
	}

	// resolveMariaDBRootPassword & resolveMariaDBSocket
	explicit := resolveMariaDBRootPassword(bench, "my-pass")
	if explicit != "my-pass" {
		t.Errorf("expected explicit password, got %s", explicit)
	}
	_ = resolveMariaDBSocket(bench)
}

func TestCoreSiteHealthExtended(t *testing.T) {
	c := SiteCheck{State: SiteHealthy}
	if d := c.Describe("mysite"); d != "mysite already exists and works" {
		t.Errorf("unexpected Describe: %s", d)
	}
	c2 := SiteCheck{State: SiteIncomplete, Reason: "no database"}
	if d := c2.Describe("mysite"); d == "" {
		t.Error("Describe should not be empty for incomplete")
	}

	// lastLine
	if lastLine("") != "" {
		t.Error("lastLine empty failed")
	}
	if lastLine("a\nb\nc\n") != "c" {
		t.Errorf("expected c, got %s", lastLine("a\nb\nc\n"))
	}
}

func TestCoreServicesExtended(t *testing.T) {
	// StateLabel
	if StateLabel(StateRunning) != "● running" || StateLabel(StateStopped) != "○ stopped" || StateLabel(StateFailed) != "✖ failed" {
		t.Errorf("StateLabel unexpected: %s, %s, %s", StateLabel(StateRunning), StateLabel(StateStopped), StateLabel(StateFailed))
	}

	sup := NewSupervisor()
	if sup == nil {
		t.Fatal("NewSupervisor returned nil")
	}
	_, _ = sup.Status()
}

func TestCoreFPMClientExtended(t *testing.T) {
	fpm := NewFPMClient()
	if fpm == nil {
		t.Fatal("NewFPMClient returned nil")
	}

	pkg := FPMPackage{Version: "1.2.3"}
	if pkg.DisplayVersion() != "1.2.3" {
		t.Errorf("DisplayVersion failed for non-empty")
	}
	pkgEmpty := FPMPackage{Version: ""}
	if pkgEmpty.DisplayVersion() != "latest" {
		t.Errorf("DisplayVersion failed for empty")
	}

	if CompareVersions("1.2.0", "1.1.0") <= 0 {
		t.Error("1.2.0 should be greater than 1.1.0")
	}
	if CompareVersions("1.0.0", "2.0.0") >= 0 {
		t.Error("1.0.0 should be less than 2.0.0")
	}
	if CompareVersions("1.0.0", "1.0.0") != 0 {
		t.Error("1.0.0 should equal 1.0.0")
	}

	// fpmConfigPath with snap envs
	t.Setenv("SNAP", "/snap/vybench/current")
	t.Setenv("SNAP_USER_COMMON", "/var/snap/vybench/user-common")
	_ = fpmConfigPath()
	t.Setenv("SNAP_USER_COMMON", "")
	t.Setenv("SNAP_COMMON", "/var/snap/vybench/common")
	_ = fpmConfigPath()
	t.Setenv("SNAP", "")
}

func TestCoreHealthAndOps(t *testing.T) {
	bench := t.TempDir()
	_ = os.MkdirAll(filepath.Join(bench, "sites"), 0755)
	_ = os.WriteFile(filepath.Join(bench, "sites", "common_site_config.json"), []byte(`{"webserver_port": 8000}`), 0644)

	// CheckBench and ServedBench
	pidDir := filepath.Join(bench, "config", "pids")
	_ = os.MkdirAll(pidDir, 0755)
	_ = os.WriteFile(filepath.Join(pidDir, "web.pid"), []byte("99999999\n"), 0644)

	bh := CheckBench(bench, "mariadb", []string{bench})
	if bh.WebPort != 8000 {
		t.Errorf("expected WebPort 8000, got %d", bh.WebPort)
	}

	// CreateLinked
	mgr := &Manager{VarDir: filepath.Join(bench, "var"), ConfigDir: filepath.Join(bench, "cfg")}
	plan := BenchPlan{
		NewBenchOptions: NewBenchOptions{
			Name:          "newb",
			DBEngine:      "mariadb",
			FrappeVersion: "v16.34.1",
		},
		Path: filepath.Join(bench, "var", "benches", "newb"),
	}
	if err := mgr.CreateLinked(plan); err != nil {
		t.Fatalf("CreateLinked failed: %v", err)
	}

	// InitBenchSpec
	initPlan := BenchPlan{
		NewBenchOptions: NewBenchOptions{
			Name:          "initb",
			DBEngine:      "postgres",
			FrappeVersion: "v15.0.0",
		},
		Path:      filepath.Join(bench, "var", "benches", "initb"),
		Branch:    "version-15",
		NeedsInit: true,
	}
	spec, err := mgr.InitBenchSpec(initPlan, plan.Path)
	if err == nil {
		if len(spec.Steps) != 3 {
			t.Errorf("expected 3 steps in InitBenchSpec, got %d", len(spec.Steps))
		}
	}

	// StartDatabaseSteps
	sup := NewSupervisor()
	sup.platform = PlatformSnap
	stepsSnap, err := sup.StartDatabaseSteps(bench, "postgres")
	if err != nil || len(stepsSnap) == 0 {
		t.Errorf("StartDatabaseSteps failed for Snap postgres: %v", err)
	}

	sup.platform = PlatformNative
	stepsNat, err := sup.StartDatabaseSteps(bench, "postgres")
	if err != nil || len(stepsNat) == 0 {
		t.Errorf("StartDatabaseSteps failed for Native postgres: %v", err)
	}

	// DropSiteSpec incomplete DB missing
	incompleteCheck := SiteCheck{State: SiteIncomplete, DBMissing: true}
	dropSpec, err := DropSiteSpec(bench, "incomplete.localhost", false, incompleteCheck)
	if err != nil || len(dropSpec.Steps) == 0 {
		t.Errorf("DropSiteSpec failed for incomplete: %v", err)
	}
	_ = dropSpec.Steps[0].Fn(func(string) {})

	// DropSiteSpec with force
	_, _ = DropSiteSpec(bench, "normal.localhost", true, SiteCheck{State: SiteHealthy})

	// RestoreSpec validations
	// invalid site name
	if _, err := RestoreSpec(bench, RestoreOptions{Site: "bad site"}); err == nil {
		t.Error("expected error for bad site name")
	}
	// empty sql path
	if _, err := RestoreSpec(bench, RestoreOptions{Site: "site.localhost", SQLPath: ""}); err == nil {
		t.Error("expected error for empty sql path")
	}
	// non-existent sql file
	if _, err := RestoreSpec(bench, RestoreOptions{Site: "site.localhost", SQLPath: "/tmp/nonexistent.sql.gz"}); err == nil {
		t.Error("expected error for non-existent sql path")
	}

	// valid sql file with full flags
	sqlFile := filepath.Join(bench, "backup.sql.gz")
	_ = os.WriteFile(sqlFile, []byte("fake"), 0644)
	pubFile := filepath.Join(bench, "files.tar")
	_ = os.WriteFile(pubFile, []byte("fake"), 0644)
	privFile := filepath.Join(bench, "private-files.tar")
	_ = os.WriteFile(privFile, []byte("fake"), 0644)

	rSpec, err := RestoreSpec(bench, RestoreOptions{
		Site:           "site.localhost",
		SQLPath:        sqlFile,
		PublicFiles:    pubFile,
		PrivateFiles:   privFile,
		Force:          true,
		DBRootUser:     "root",
		DBRootPassword: "pw",
	})
	if err != nil {
		t.Fatalf("RestoreSpec failed: %v", err)
	}
	if len(rSpec.Steps) == 0 {
		t.Error("RestoreSpec returned empty steps")
	}

	// parseSystemdShow
	systemctlOutput := `Id=mariadb.service
LoadState=loaded
ActiveState=active
UnitFileState=enabled

Id=frappe-web.service
LoadState=loaded
ActiveState=inactive
UnitFileState=disabled

Id=frappe-worker.service
LoadState=not-found
ActiveState=inactive
UnitFileState=disabled
`
	svcs := parseSystemdShow([]byte(systemctlOutput))
	if len(svcs) != 2 {
		t.Errorf("expected 2 services from parseSystemdShow, got %d", len(svcs))
	}
	if svcs[0].State != StateRunning || svcs[1].State != StateStopped {
		t.Errorf("unexpected states: %v, %v", svcs[0].State, svcs[1].State)
	}

	// isDatastore
	for _, name := range []string{"vybench.mariadb", "vybench.redis", "postgresql@16", "frappe-web"} {
		_ = isDatastore(name)
	}
}

func TestAtomicAndConfigHelpers(t *testing.T) {
	tmp := t.TempDir()

	// writeFileAtomic
	fpath := filepath.Join(tmp, "sub", "test.txt")
	if err := writeFileAtomic(fpath, []byte("atomic content"), 0644); err != nil {
		t.Fatalf("writeFileAtomic failed: %v", err)
	}
	data, _ := os.ReadFile(fpath)
	if string(data) != "atomic content" {
		t.Errorf("expected atomic content, got %s", string(data))
	}

	// replaceSymlink
	target1 := filepath.Join(tmp, "t1")
	target2 := filepath.Join(tmp, "t2")
	_ = os.WriteFile(target1, []byte("1"), 0644)
	_ = os.WriteFile(target2, []byte("2"), 0644)
	link := filepath.Join(tmp, "current")

	if err := replaceSymlink(target1, link); err != nil {
		t.Fatalf("replaceSymlink 1 failed: %v", err)
	}
	if err := replaceSymlink(target2, link); err != nil {
		t.Fatalf("replaceSymlink 2 failed: %v", err)
	}

	// prepareBenchesDir
	if err := prepareBenchesDir(filepath.Join(tmp, "benches")); err != nil {
		t.Fatalf("prepareBenchesDir failed: %v", err)
	}

	// parseListApps
	jsonText := `{"site1.localhost": ["frappe", "erpnext"]}`
	apps, ok := parseListApps(jsonText, "site1.localhost")
	if !ok || len(apps) != 2 || apps[0] != "frappe" {
		t.Errorf("parseListApps exact failed: %v, %v", ok, apps)
	}
	apps2, ok2 := parseListApps(jsonText, "other.localhost")
	if !ok2 || len(apps2) != 2 {
		t.Errorf("parseListApps fallback failed: %v, %v", ok2, apps2)
	}
	if _, ok3 := parseListApps("plain text not json", "site"); ok3 {
		t.Error("parseListApps should fail on non-json")
	}

	// ensureBenchID
	mgr := &Manager{VarDir: filepath.Join(tmp, "var")}
	benchPath := filepath.Join(tmp, "mybench")
	// Case 1: no common_site_config.json
	if err := mgr.ensureBenchID(benchPath, "bench-id-1"); err != nil {
		t.Fatalf("ensureBenchID (missing) failed: %v", err)
	}
	// Case 2: already has bench_id
	if err := mgr.ensureBenchID(benchPath, "bench-id-2"); err != nil {
		t.Fatalf("ensureBenchID (existing) failed: %v", err)
	}
	cfg, _ := readConfig(commonConfigPath(benchPath))
	if cfg["bench_id"] != "bench-id-1" {
		t.Errorf("ensureBenchID overwrote existing id: %v", cfg["bench_id"])
	}

	// PackagedFrappeVersion
	mgr.PackagedPath = ""
	if mgr.PackagedFrappeVersion() != "" {
		t.Error("PackagedFrappeVersion should be empty when PackagedPath is empty")
	}
}

func TestSiteHealthAndChecks(t *testing.T) {
	// classifySiteCheck branches
	// 1: success but no frappe app
	c1 := classifySiteCheck("site.localhost", []byte(`{"site.localhost": ["otherapp"]}`), nil)
	if c1.State != SiteIncomplete || c1.Reason != "frappe is not installed on it" {
		t.Errorf("c1 unexpected: %+v", c1)
	}

	// 2: success but unreadable json
	c2 := classifySiteCheck("site.localhost", []byte(`not-json`), nil)
	if c2.State != SiteUnknown {
		t.Errorf("c2 unexpected: %+v", c2)
	}

	// 3: error with unknown database (1049)
	c3 := classifySiteCheck("site.localhost", []byte(`ERROR 1049 (42000): Unknown database '_test'`), errors.New("exit 1"))
	if c3.State != SiteIncomplete || !c3.DBMissing {
		t.Errorf("c3 unexpected: %+v", c3)
	}

	// 4: error with table doesn't exist (1146)
	c4 := classifySiteCheck("site.localhost", []byte(`ERROR 1146 (42S02): Table '_test.tabDocType' doesn't exist`), errors.New("exit 1"))
	if c4.State != SiteIncomplete || c4.DBMissing {
		t.Errorf("c4 unexpected: %+v", c4)
	}

	// 5: generic error
	c5 := classifySiteCheck("site.localhost", []byte(`Some random fatal error`), errors.New("exit 1"))
	if c5.State != SiteUnknown {
		t.Errorf("c5 unexpected: %+v", c5)
	}

	// QuickSiteCheck branches
	bench := t.TempDir()
	// Case 1: no site_config.json
	qc1, ok1 := QuickSiteCheck(bench, "missing.localhost")
	if !ok1 || qc1.State != SiteIncomplete || !qc1.DBMissing {
		t.Errorf("qc1 unexpected: %v, %+v", ok1, qc1)
	}

	// Case 2: site_config.json with empty db_name
	siteDir := filepath.Join(bench, "sites", "empty-db.localhost")
	_ = os.MkdirAll(siteDir, 0755)
	_ = os.WriteFile(filepath.Join(siteDir, "site_config.json"), []byte(`{"db_name": ""}`), 0644)
	qc2, ok2 := QuickSiteCheck(bench, "empty-db.localhost")
	if !ok2 || qc2.State != SiteIncomplete || !qc2.DBMissing {
		t.Errorf("qc2 unexpected: %v, %+v", ok2, qc2)
	}

	// Case 3: site_config.json with corrupt json
	_ = os.WriteFile(filepath.Join(siteDir, "site_config.json"), []byte(`{bad json`), 0644)
	qc3, ok3 := QuickSiteCheck(bench, "empty-db.localhost")
	if !ok3 || qc3.State != SiteUnknown {
		t.Errorf("qc3 unexpected: %v, %+v", ok3, qc3)
	}
}

func TestBenchManagerAndPlatformBranches(t *testing.T) {
	tmp := t.TempDir()
	mgr := &Manager{VarDir: filepath.Join(tmp, "var"), ConfigDir: filepath.Join(tmp, "cfg")}

	// orUnknown
	if orUnknown("") != "none found" || orUnknown("v16") != "v16" {
		t.Errorf("orUnknown unexpected: %s", orUnknown(""))
	}

	// DefaultFrappeVersion
	mgr.PackagedPath = ""
	if v := mgr.DefaultFrappeVersion(); v != "16" {
		t.Errorf("expected default 16, got %s", v)
	}

	// WebserverPort
	benchDir := filepath.Join(tmp, "bench")
	if port := WebserverPort(benchDir); port != 8000 {
		t.Errorf("expected 8000, got %d", port)
	}

	// SwitchBench non-existent dir error
	reg := &BenchRegistry{
		ActiveBench: "b1",
		Benches: map[string]BenchEntry{
			"b1": {Path: filepath.Join(tmp, "nonexistent")},
		},
	}
	_ = mgr.SaveRegistry(reg)
	if err := mgr.SwitchBench("b1"); err == nil {
		t.Error("expected error switching to nonexistent bench dir")
	}

	// DropBench active bench error
	if err := mgr.DropBench("b1"); err == nil {
		t.Error("expected error dropping active bench")
	}

	// AttachBench validation errors
	if err := mgr.AttachBench("bad bench name!", benchDir, "", ""); err == nil {
		t.Error("expected error attaching invalid bench name")
	}
	if err := mgr.AttachBench("validname", filepath.Join(tmp, "nonexistent"), "", ""); err == nil {
		t.Error("expected error attaching nonexistent directory")
	}

	// Platform detection
	t.Setenv("SNAP", "")
	t.Setenv("VYBENCH_HOMEBREW", "1")
	_ = DetectPlatform()
	_ = PackagedBenchPath()
	t.Setenv("VYBENCH_HOMEBREW", "")
}

func TestSupervisorControlStepsAndStatus(t *testing.T) {
	sup := NewSupervisor()

	// ControlSteps invalid action
	if _, err := sup.ControlSteps("invalid_action"); err == nil {
		t.Error("expected error for invalid action")
	}

	// PlatformSnap ControlSteps
	sup.platform = PlatformSnap
	for _, act := range []string{"start", "stop", "restart"} {
		_, _ = sup.ControlSteps(act)
	}
	_, _ = sup.Status()

	// PlatformNative ControlSteps & Status
	sup.platform = PlatformNative
	for _, act := range []string{"start", "stop", "restart"} {
		steps, err := sup.ControlSteps(act)
		if err != nil || len(steps) == 0 {
			t.Errorf("expected steps for native %s: %v", act, err)
		}
	}
	_, _ = sup.Status()
	_, _ = sup.systemdStatus()

	// PlatformUnknown
	sup.platform = PlatformUnknown
	if _, err := sup.ControlSteps("start"); err != ErrUnsupportedPlatform {
		t.Errorf("expected ErrUnsupportedPlatform, got: %v", err)
	}
	if _, err := sup.Status(); err != ErrUnsupportedPlatform {
		t.Errorf("expected ErrUnsupportedPlatform for status, got: %v", err)
	}

	// RestartHint for all platforms
	t.Setenv("SNAP", "/snap/vybench/current")
	_ = RestartHint()
	t.Setenv("SNAP", "")

	// StateLabel
	if StateLabel(ServiceState(99)) != "? unknown" {
		t.Errorf("expected ? unknown for invalid state, got: %s", StateLabel(ServiceState(99)))
	}
}

func TestNewManagerEnvVariants(t *testing.T) {
	tmp := t.TempDir()

	t.Setenv("VYBENCH_VAR", "")
	t.Setenv("SNAP_COMMON", filepath.Join(tmp, "snap_common"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg_config"))

	m := NewManager()
	if m.VarDir != filepath.Join(tmp, "snap_common") {
		t.Errorf("expected VarDir to match SNAP_COMMON, got: %s", m.VarDir)
	}
	if m.RegistryPath() == "" || m.CurrentBenchSymlink() == "" || m.legacyRegistryPath() == "" {
		t.Error("expected non-empty registry and symlink paths")
	}
	_ = m.registryExists()
	_ = m.LegacyBenchPath()

	t.Setenv("SNAP_COMMON", "")
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestSymlinkAndFileAtomic(t *testing.T) {
	tmp := t.TempDir()

	// replaceSymlink success
	target1 := filepath.Join(tmp, "target1")
	_ = os.WriteFile(target1, []byte("one"), 0644)
	link := filepath.Join(tmp, "sub", "link")
	if err := replaceSymlink(target1, link); err != nil {
		t.Fatalf("replaceSymlink failed: %v", err)
	}
	target2 := filepath.Join(tmp, "target2")
	_ = os.WriteFile(target2, []byte("two"), 0644)
	if err := replaceSymlink(target2, link); err != nil {
		t.Fatalf("replaceSymlink update failed: %v", err)
	}

	// replaceSymlink error (directory creation failure)
	if err := replaceSymlink(target1, "/dev/null/impossible/link"); err == nil {
		t.Error("expected error for invalid link path")
	}

	// writeFileAtomic success
	outPath := filepath.Join(tmp, "atomic.txt")
	if err := writeFileAtomic(outPath, []byte("content"), 0644); err != nil {
		t.Fatalf("writeFileAtomic failed: %v", err)
	}
	data, _ := os.ReadFile(outPath)
	if string(data) != "content" {
		t.Errorf("unexpected content: %s", string(data))
	}

	// writeFileAtomic error (directory creation failure)
	if err := writeFileAtomic("/dev/null/impossible/file.txt", []byte("bad"), 0644); err == nil {
		t.Error("expected error for invalid atomic file path")
	}

	// prepareBenchesDir
	benchesDir := filepath.Join(tmp, "benches")
	if err := prepareBenchesDir(benchesDir); err != nil {
		t.Fatalf("prepareBenchesDir failed: %v", err)
	}
	if err := prepareBenchesDir("/dev/null/impossible"); err == nil {
		t.Error("expected error for impossible benches dir")
	}
}

func TestSupervisorStartOrRestartAndDatabaseSteps(t *testing.T) {
	sup := &Supervisor{platform: PlatformBrew, name: "vybench"}
	var logged []string
	logFn := func(s string) { logged = append(logged, s) }
	// startOrRestart invokes brew services command
	_ = sup.startOrRestart(logFn)

	// StartDatabaseSteps
	_, _ = sup.StartDatabaseSteps("", "mariadb")
	_, _ = sup.StartDatabaseSteps("", "postgres")
	_ = WaitForDatabaseStep("", "mariadb", 50*time.Millisecond)
	_ = WaitForDatabaseStep("", "postgres", 50*time.Millisecond)
}

func TestJobTerminateAndArchiveSite(t *testing.T) {
	// terminate with nil / unstarted
	terminate(nil)
	terminate(&exec.Cmd{})

	// terminate with running command
	cmd := exec.Command("sleep", "10")
	detach(cmd)
	if err := cmd.Start(); err == nil {
		terminate(cmd)
	}

	// archiveSite success
	tmp := t.TempDir()
	benchPath := filepath.Join(tmp, "bench")
	siteDir := filepath.Join(benchPath, "sites", "site1")
	_ = os.MkdirAll(siteDir, 0755)
	if _, err := archiveSite(benchPath, "site1"); err != nil {
		t.Fatalf("archiveSite failed: %v", err)
	}

	// archiveSite failure
	if _, err := archiveSite(benchPath, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent site")
	}
}

func TestRegistryLegacyFallbackAndMigrate(t *testing.T) {
	tmp := t.TempDir()
	mgr := &Manager{
		VarDir:    filepath.Join(tmp, "var"),
		ConfigDir: filepath.Join(tmp, "config"),
	}

	// Create legacy benches.json in ConfigDir
	legacyFile := mgr.legacyRegistryPath()
	_ = os.MkdirAll(filepath.Dir(legacyFile), 0755)
	regData := `{"active_bench":"b1","benches":{"b1":{"path":"/path/b1","db_engine":"mariadb"}}}`
	_ = os.WriteFile(legacyFile, []byte(regData), 0644)

	reg, err := mgr.LoadRegistry()
	if err != nil {
		t.Fatalf("LoadRegistry with legacy fallback failed: %v", err)
	}
	if reg.ActiveBench != "b1" || len(reg.Benches) != 1 {
		t.Errorf("unexpected registry from legacy: %+v", reg)
	}

	// migrateLegacy when legacy bench has sites/
	legacyBench := filepath.Join(tmp, "legacy-bench")
	_ = os.MkdirAll(filepath.Join(legacyBench, "sites"), 0755)
	mgr.VarDir = filepath.Join(tmp, "var2")
	_ = os.MkdirAll(mgr.VarDir, 0755)

	emptyReg := &BenchRegistry{
		ActiveBench: "default",
		Benches: map[string]BenchEntry{
			"default": {Path: legacyBench},
		},
	}
	t.Setenv("VYBENCH_BENCH", legacyBench)
	_ = mgr.migrateLegacy(emptyReg)

	// migrateLegacy when legacy bench has no sites/
	emptyReg2 := &BenchRegistry{
		ActiveBench: "default",
		Benches: map[string]BenchEntry{
			"default": {Path: filepath.Join(tmp, "nonexistent")},
		},
	}
	_ = mgr.migrateLegacy(emptyReg2)
	if _, ok := emptyReg2.Benches["default"]; ok {
		t.Error("expected default bench removed when legacy bench has no sites")
	}
	_ = os.Unsetenv("VYBENCH_BENCH")
}

func TestCheckSiteAndInstalledAppsMock(t *testing.T) {
	tmp := t.TempDir()
	benchPath := filepath.Join(tmp, "bench")

	// 1. InstalledApps with apps.txt
	sitesDir := filepath.Join(benchPath, "sites")
	_ = os.MkdirAll(sitesDir, 0755)
	_ = os.WriteFile(filepath.Join(sitesDir, "apps.txt"), []byte("frappe\ncustom_app\n"), 0644)
	apps := InstalledApps(benchPath)
	if _, ok := apps["frappe"]; !ok {
		t.Errorf("expected frappe in InstalledApps, got: %v", apps)
	}

	// 2. InstalledApps without apps.txt (reading apps/ directory)
	benchPath2 := filepath.Join(tmp, "bench2")
	_ = os.MkdirAll(filepath.Join(benchPath2, "apps", "erpnext"), 0755)
	apps2 := InstalledApps(benchPath2)
	if _, ok := apps2["erpnext"]; !ok {
		t.Errorf("expected erpnext in InstalledApps, got: %v", apps2)
	}

	// 3. FindFPM with VYBENCH_FPM
	fpmBin := filepath.Join(tmp, "mock-fpm")
	_ = os.WriteFile(fpmBin, []byte("#!/bin/sh\nexit 0\n"), 0755)
	t.Setenv("VYBENCH_FPM", fpmBin)
	found, err := FindFPM()
	if err != nil || found != fpmBin {
		t.Errorf("FindFPM failed: %v, found=%s", err, found)
	}
	_ = fpmConfigPath()
	_ = os.Unsetenv("VYBENCH_FPM")

	// 4. CheckSite with mock VYBENCH_CLI returning healthy JSON
	mockCLIHealthy := filepath.Join(tmp, "mock-vybench-healthy")
	_ = os.WriteFile(mockCLIHealthy, []byte("#!/bin/sh\necho '{\"site1\": [\"frappe\", \"erpnext\"]}'\n"), 0755)
	t.Setenv("VYBENCH_CLI", mockCLIHealthy)

	site1Dir := filepath.Join(benchPath, "sites", "site1")
	_ = os.MkdirAll(site1Dir, 0755)
	_ = os.WriteFile(filepath.Join(site1Dir, "site_config.json"), []byte(`{"db_name": "site1db"}`), 0644)

	res := CheckSite(benchPath, "site1")
	if res.State != SiteHealthy {
		t.Errorf("expected SiteHealthy, got: %+v", res)
	}

	// 5. CheckSite with mock VYBENCH_CLI returning database missing error
	mockCLIErr := filepath.Join(tmp, "mock-vybench-err")
	_ = os.WriteFile(mockCLIErr, []byte("#!/bin/sh\necho 'database \"site2db\" does not exist' >&2\nexit 1\n"), 0755)
	t.Setenv("VYBENCH_CLI", mockCLIErr)

	site2Dir := filepath.Join(benchPath, "sites", "site2")
	_ = os.MkdirAll(site2Dir, 0755)
	_ = os.WriteFile(filepath.Join(site2Dir, "site_config.json"), []byte(`{"db_name": "site2db"}`), 0644)

	res2 := CheckSite(benchPath, "site2")
	if res2.State != SiteIncomplete || !res2.DBMissing {
		t.Errorf("expected SiteIncomplete with DBMissing, got: %+v", res2)
	}

	// 6. CheckSite without frappe in apps
	mockCLINoFrappe := filepath.Join(tmp, "mock-vybench-nofrappe")
	_ = os.WriteFile(mockCLINoFrappe, []byte("#!/bin/sh\necho '{\"site3\": [\"customapp\"]}'\n"), 0755)
	t.Setenv("VYBENCH_CLI", mockCLINoFrappe)

	site3Dir := filepath.Join(benchPath, "sites", "site3")
	_ = os.MkdirAll(site3Dir, 0755)
	_ = os.WriteFile(filepath.Join(site3Dir, "site_config.json"), []byte(`{"db_name": "site3db"}`), 0644)
	res3 := CheckSite(benchPath, "site3")
	if res3.State != SiteIncomplete || !strings.Contains(res3.Reason, "frappe is not installed") {
		t.Errorf("expected SiteIncomplete (no frappe), got: %+v", res3)
	}

	// 7. CheckSite with table missing (1146 error)
	mockCLI1146 := filepath.Join(tmp, "mock-vybench-1146")
	_ = os.WriteFile(mockCLI1146, []byte("#!/bin/sh\necho 'Error: (1146, Table doesn\\'t exist)' >&2\nexit 1\n"), 0755)
	t.Setenv("VYBENCH_CLI", mockCLI1146)
	res4 := CheckSite(benchPath, "site3")
	if res4.State != SiteIncomplete || !strings.Contains(res4.Reason, "partly installed") {
		t.Errorf("expected SiteIncomplete (partly installed), got: %+v", res4)
	}

	// 8. CheckSite when BenchCommand fails (no bench binary or VYBENCH_CLI)
	_ = os.Unsetenv("VYBENCH_CLI")
	benchNoCLI := filepath.Join(tmp, "bench-nocli")
	siteNoCLIDir := filepath.Join(benchNoCLI, "sites", "sitenocli")
	_ = os.MkdirAll(siteNoCLIDir, 0755)
	_ = os.WriteFile(filepath.Join(siteNoCLIDir, "site_config.json"), []byte(`{"db_name": "sitenoclidb"}`), 0644)
	res5 := CheckSite(benchNoCLI, "sitenocli")
	if res5.State != SiteUnknown {
		t.Errorf("expected SiteUnknown when bench command fails, got: %+v", res5)
	}
}

func TestPlatformAndHealthEdgeCases(t *testing.T) {
	// processAlive
	if !processAlive(os.Getpid()) {
		t.Error("expected current process to be alive")
	}
	if processAlive(99999999) {
		t.Error("expected non-existent pid not to be alive")
	}

	// runCaptured timeout
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = runCaptured(ctx, exec.Command("sleep", "5"))

	// SiteCheck Describe
	sc1 := SiteCheck{State: SiteHealthy}
	if !strings.Contains(sc1.Describe("site1"), "works") {
		t.Errorf("unexpected describe: %s", sc1.Describe("site1"))
	}
	sc2 := SiteCheck{State: SiteIncomplete, Reason: "broken"}
	if !strings.Contains(sc2.Describe("site2"), "broken") {
		t.Errorf("unexpected describe: %s", sc2.Describe("site2"))
	}
	sc3 := SiteCheck{State: SiteUnknown, Reason: "cannot connect"}
	if !strings.Contains(sc3.Describe("site3"), "cannot connect") {
		t.Errorf("unexpected describe: %s", sc3.Describe("site3"))
	}

	// WebserverPort
	tmp := t.TempDir()
	benchPath := filepath.Join(tmp, "bench")
	_ = os.MkdirAll(filepath.Join(benchPath, "sites"), 0755)
	if p := WebserverPort(benchPath); p != 8000 {
		t.Errorf("expected 8000, got %d", p)
	}
	cfgFile := filepath.Join(benchPath, "sites", "common_site_config.json")
	_ = os.WriteFile(cfgFile, []byte(`{"webserver_port": 9005}`), 0644)
	if p := WebserverPort(benchPath); p != 9005 {
		t.Errorf("expected 9005, got %d", p)
	}
	_ = os.WriteFile(cfgFile, []byte(`{"webserver_port": "9006"}`), 0644)
	if p := WebserverPort(benchPath); p != 9006 {
		t.Errorf("expected 9006, got %d", p)
	}

	// DefaultFrappeVersion env override
	m := NewManager()
	t.Setenv("VYBENCH_DEFAULT_FRAPPE_VERSION", "17")
	if m.DefaultFrappeVersion() != "17" {
		t.Errorf("expected 17 from env, got %s", m.DefaultFrappeVersion())
	}
	_ = os.Unsetenv("VYBENCH_DEFAULT_FRAPPE_VERSION")

	// DetectFrappeVersion with git HEAD
	gitBench := filepath.Join(tmp, "gitbench")
	gitHeadDir := filepath.Join(gitBench, "apps", "frappe", ".git")
	_ = os.MkdirAll(gitHeadDir, 0755)
	_ = os.WriteFile(filepath.Join(gitHeadDir, "HEAD"), []byte("ref: refs/heads/version-15\n"), 0644)
	if v := DetectFrappeVersion(gitBench); v != "15" {
		t.Errorf("expected 15 from git HEAD, got %s", v)
	}

	// siteDatabaseDir under Snap
	t.Setenv("SNAP", tmp)
	t.Setenv("SNAP_COMMON", tmp)
	snapSock := filepath.Join(tmp, "run", "mysql.sock")
	_ = os.MkdirAll(filepath.Join(tmp, "mariadb", "mysql"), 0755)
	_ = os.WriteFile(cfgFile, []byte(fmt.Sprintf(`{"db_socket": "%s"}`, snapSock)), 0644)
	dir, ok := siteDatabaseDir(benchPath, "s1", "s1_db")
	if !ok || !strings.Contains(dir, "s1_db") {
		t.Errorf("expected siteDatabaseDir success, got ok=%v, dir=%s", ok, dir)
	}
	t.Setenv("SNAP", "")
	t.Setenv("SNAP_COMMON", "")
}

func TestRestoreSpecAndBenchJob(t *testing.T) {
	tmp := t.TempDir()
	benchPath := filepath.Join(tmp, "bench")
	_ = os.MkdirAll(filepath.Join(benchPath, "sites"), 0755)

	mockCLI := filepath.Join(tmp, "mock-cli")
	_ = os.WriteFile(mockCLI, []byte("#!/bin/sh\nexit 0\n"), 0755)
	t.Setenv("VYBENCH_CLI", mockCLI)

	// benchJob
	spec, err := benchJob(benchPath, "my label", "version")
	if err != nil || len(spec.Steps) == 0 {
		t.Fatalf("benchJob failed: %v", err)
	}

	// RestoreSpec validation errors
	_, err = RestoreSpec(benchPath, RestoreOptions{Site: ""})
	if err == nil {
		t.Error("expected error for empty site")
	}
	_, err = RestoreSpec(benchPath, RestoreOptions{Site: "site1", SQLPath: ""})
	if err == nil {
		t.Error("expected error for empty sql path")
	}
	_, err = RestoreSpec(benchPath, RestoreOptions{Site: "site1", SQLPath: "/nonexistent.sql.gz"})
	if err == nil {
		t.Error("expected error for nonexistent sql file")
	}

	// RestoreSpec success with options
	sqlFile := filepath.Join(tmp, "backup.sql.gz")
	_ = os.WriteFile(sqlFile, []byte("sql"), 0644)
	pubFile := filepath.Join(tmp, "files.tar")
	_ = os.WriteFile(pubFile, []byte("tar"), 0644)
	privFile := filepath.Join(tmp, "private.tar")
	_ = os.WriteFile(privFile, []byte("tar"), 0644)

	resSpec, err := RestoreSpec(benchPath, RestoreOptions{
		Site:           "site1",
		SQLPath:        sqlFile,
		PublicFiles:    pubFile,
		PrivateFiles:   privFile,
		Force:          true,
		DBRootUser:     "root",
		DBRootPassword: "pwd",
	})
	if err != nil {
		t.Fatalf("RestoreSpec failed: %v", err)
	}
	if len(resSpec.Steps) == 0 {
		t.Error("expected restore steps")
	}

	_ = os.Unsetenv("VYBENCH_CLI")
}

func TestManagerRegistryAndBenchLifecycle(t *testing.T) {
	tmp := t.TempDir()
	mgr := &Manager{
		VarDir:    filepath.Join(tmp, "var"),
		ConfigDir: filepath.Join(tmp, "config"),
	}

	// 1. CreateLinked with impossible directory
	planBad := BenchPlan{
		NewBenchOptions: NewBenchOptions{Name: "bad"},
		Path:            "/dev/null/impossible/bad",
	}
	if err := mgr.CreateLinked(planBad); err == nil {
		t.Error("expected error for impossible bench path in CreateLinked")
	}

	// 2. CreateLinked with register error
	planRegFail := BenchPlan{
		NewBenchOptions: NewBenchOptions{Name: "regfail"},
		Path:            filepath.Join(tmp, "regfail"),
	}
	mgrBadVar := &Manager{
		VarDir:       "/dev/null/impossible",
		ConfigDir:    filepath.Join(tmp, "config"),
		PackagedPath: filepath.Join(tmp, "packaged"),
	}
	if err := mgrBadVar.CreateLinked(planRegFail); err == nil {
		t.Error("expected error for register failure in CreateLinked")
	}

	// 3. AttachBench branches
	benchDir1 := filepath.Join(tmp, "bench1")
	_ = os.MkdirAll(filepath.Join(benchDir1, "sites"), 0755)
	if err := mgr.AttachBench("bench1", benchDir1, "mariadb", ""); err != nil {
		t.Fatalf("AttachBench bench1 failed: %v", err)
	}

	// Attach duplicate name
	if err := mgr.AttachBench("bench1", benchDir1, "mariadb", ""); err == nil {
		t.Error("expected error attaching duplicate bench name")
	}

	// Attach duplicate path under different name
	if err := mgr.AttachBench("bench1_dup", benchDir1, "mariadb", ""); err == nil {
		t.Error("expected error attaching duplicate bench path")
	}

	// 4. Bench lookup with detected version
	frappeDir := filepath.Join(benchDir1, "apps", "frappe", "frappe")
	_ = os.MkdirAll(frappeDir, 0755)
	_ = os.WriteFile(filepath.Join(frappeDir, "__init__.py"), []byte("__version__ = '16.5.0'\n"), 0644)
	ab, err := mgr.Bench("bench1")
	if err != nil || ab.Entry.FrappeVersion != "16.5.0" {
		t.Errorf("expected version 16.5.0 from Bench, got %+v, err=%v", ab, err)
	}

	// 5. Drop active bench error
	if err := mgr.SwitchBench("bench1"); err != nil {
		t.Fatalf("SwitchBench failed: %v", err)
	}
	if err := mgr.DropBench("bench1"); err == nil {
		t.Error("expected error dropping active bench")
	}

	// 6. SwitchBench to a bench whose path no longer exists
	benchDirGhost := filepath.Join(tmp, "ghost")
	_ = os.MkdirAll(filepath.Join(benchDirGhost, "sites"), 0755)
	if err := mgr.AttachBench("ghost", benchDirGhost, "mariadb", ""); err != nil {
		t.Fatalf("AttachBench ghost failed: %v", err)
	}
	_ = os.RemoveAll(benchDirGhost)
	if err := mgr.SwitchBench("ghost"); err == nil {
		t.Error("expected error switching to deleted bench path")
	}
}

func TestHelpersAndUtilities(t *testing.T) {
	// 1. writeConfig failure
	if err := writeConfig("/dev/null/impossible/cfg.json", map[string]any{"a": 1}); err == nil {
		t.Error("expected error for impossible writeConfig path")
	}

	// 2. sanitize
	s := sanitize("hello\tworld\x07\x7f\x1b[31mred\x1b[0m")
	if !strings.Contains(s, "hello    world") || strings.Contains(s, "\x07") || strings.Contains(s, "\x1b") {
		t.Errorf("unexpected sanitized string: %q", s)
	}

	// 3. humanSize
	if humanSize(2*(1<<30)) != "2.0 GB" {
		t.Errorf("expected 2.0 GB, got %s", humanSize(2*(1<<30)))
	}
	if humanSize(50*(1<<20)) != "50.0 MB" {
		t.Errorf("expected 50.0 MB, got %s", humanSize(50*(1<<20)))
	}
	if humanSize(500*(1<<10)) != "500 KB" {
		t.Errorf("expected 500 KB, got %s", humanSize(500*(1<<10)))
	}
	if humanSize(123) != "123 B" {
		t.Errorf("expected 123 B, got %s", humanSize(123))
	}

	// 4. ExpandHome
	p1, _ := ExpandHome("~")
	p2, _ := ExpandHome("~/my/path")
	p3, _ := ExpandHome("/tmp/plain")
	if p1 == "~" || strings.HasPrefix(p2, "~") || p3 != "/tmp/plain" {
		t.Errorf("unexpected ExpandHome results: %s, %s, %s", p1, p2, p3)
	}

	// 5. DisplayCommand password masking
	cmd := exec.Command("bench", "--db-root-password=supersecret", "--admin-password", "adminsecret", "status")
	displayed := DisplayCommand(cmd)
	if strings.Contains(displayed, "supersecret") || strings.Contains(displayed, "adminsecret") {
		t.Errorf("passwords not masked: %s", displayed)
	}
	if !strings.Contains(displayed, "****") {
		t.Errorf("expected mask in command: %s", displayed)
	}

	// 6. resolveMariaDBRootPassword and resolveMariaDBSocket
	tmp := t.TempDir()
	benchPath := filepath.Join(tmp, "bench")
	_ = os.MkdirAll(filepath.Join(benchPath, "sites"), 0755)

	// explicit password
	if pw := resolveMariaDBRootPassword("", "mysecret"); pw != "mysecret" {
		t.Errorf("expected explicit password, got %s", pw)
	}

	// snap root_password file
	t.Setenv("SNAP_COMMON", tmp)
	snapPwFile := filepath.Join(tmp, "mariadb", "root_password")
	_ = os.MkdirAll(filepath.Dir(snapPwFile), 0755)
	_ = os.WriteFile(snapPwFile, []byte("snapsecret\n"), 0600)
	if pw := resolveMariaDBRootPassword("", ""); pw != "snapsecret" {
		t.Errorf("expected snap secret password, got %s", pw)
	}

	// snap socket file
	snapSockFile := filepath.Join(tmp, "run", "mysql.sock")
	_ = os.MkdirAll(filepath.Dir(snapSockFile), 0755)
	_ = os.WriteFile(snapSockFile, []byte(""), 0600)
	if sock := resolveMariaDBSocket(""); sock != snapSockFile {
		t.Errorf("expected snap socket file, got %s", sock)
	}

	// from bench config
	_ = os.Remove(snapPwFile)
	_ = os.Remove(snapSockFile)
	benchConfigDir := filepath.Join(benchPath, "config")
	_ = os.MkdirAll(benchConfigDir, 0755)
	_ = os.WriteFile(filepath.Join(benchConfigDir, "mariadb_root_password"), []byte("cfgsecret"), 0600)
	cfgPath := filepath.Join(benchPath, "sites", "common_site_config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"db_socket": "/custom/sock"}`), 0644)
	if pw := resolveMariaDBRootPassword(benchPath, ""); pw != "cfgsecret" {
		t.Errorf("expected cfg secret password, got %s", pw)
	}
	if sock := resolveMariaDBSocket(benchPath); sock != "/custom/sock" {
		t.Errorf("expected custom socket, got %s", sock)
	}
	t.Setenv("SNAP_COMMON", "")

	// explicit
	if pw := ResolveMariaDBRootPassword("", "explicit_pw"); pw != "explicit_pw" {
		t.Errorf("expected explicit_pw, got %s", pw)
	}

	// from .vybench_credentials
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	_ = os.WriteFile(filepath.Join(homeDir, ".vybench_credentials"), []byte("Other info\nMariaDB Root Password: cred_password\n"), 0600)
	if pw := ResolveMariaDBRootPassword(t.TempDir(), ""); pw != "cred_password" {
		t.Errorf("expected cred_password, got %s", pw)
	}

	m := NewManager()
	if name := m.NameFor("/tmp/unregistered_bench_xyz"); name != "unregistered_bench_xyz" {
		t.Errorf("expected unregistered_bench_xyz, got %s", name)
	}
	if err := m.SwitchBench("non_existent_bench_xyz"); err == nil {
		t.Errorf("expected error switching to non-existent bench")
	}
}

func TestServiceStatesAndInitPython(t *testing.T) {
	// StateLabel all branches
	for _, st := range []ServiceState{StateRunning, StateStopped, StateFailed, StateConflict, ServiceState(99)} {
		lbl := StateLabel(st)
		if lbl == "" {
			t.Errorf("empty StateLabel for %v", st)
		}
	}

	// brewState branches
	for status, expected := range map[string]ServiceState{
		"scheduled": StateRunning,
		"error":     StateFailed,
		"stopped":   StateStopped,
		"none":      StateStopped,
		"other":     StateUnknown,
	} {
		if s := brewState(status); s != expected {
			t.Errorf("brewState(%q): expected %v, got %v", status, expected, s)
		}
	}

	// parseBrewServices with custom status
	jsonServices := `[{"name": "vybench", "status": "waiting"}]`
	svcs, err := parseBrewServices([]byte(jsonServices), "vybench")
	if err != nil || len(svcs) == 0 || svcs[0].Detail != "waiting" {
		t.Errorf("unexpected parseBrewServices result: %+v, err=%v", svcs, err)
	}

	// initPython with executable python3.14
	tmp := t.TempDir()
	t.Setenv("SNAP", tmp)
	pyBin := filepath.Join(tmp, "usr", "bin", "python3.14")
	_ = os.MkdirAll(filepath.Dir(pyBin), 0755)
	_ = os.WriteFile(pyBin, []byte("#!/bin/sh\n"), 0755)
	if p := initPython(); p != pyBin {
		t.Errorf("expected initPython %s, got %s", pyBin, p)
	}
	t.Setenv("SNAP", "")

	// vybenchCLI
	_ = vybenchCLI()
}

func TestSupervisorBrewAndSnapMockedOutput(t *testing.T) {
	// 1. brewStatus with postgresql service
	supBrew := &Supervisor{
		platform: PlatformBrew,
		name:     "vybench",
		output: func(name string, args ...string) ([]byte, error) {
			return []byte(`[
				{"name": "vybench", "status": "started"},
				{"name": "postgresql@16", "status": "started"}
			]`), nil
		},
	}
	svcs, err := supBrew.brewStatus()
	if err != nil || len(svcs) < 2 {
		t.Fatalf("brewStatus failed: %v, svcs=%+v", err, svcs)
	}

	// 2. brewStatus with error
	supErr := &Supervisor{
		platform: PlatformBrew,
		name:     "vybench",
		output: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("brew command failed")
		},
	}
	if _, err := supErr.brewStatus(); err == nil {
		t.Error("expected error from brewStatus on output failure")
	}

	// 3. brewStatus with invalid JSON
	supBadJSON := &Supervisor{
		platform: PlatformBrew,
		name:     "vybench",
		output: func(name string, args ...string) ([]byte, error) {
			return []byte("invalid json"), nil
		},
	}
	if _, err := supBadJSON.brewStatus(); err == nil {
		t.Error("expected error from brewStatus on bad JSON")
	}

	// 4. snapStatus with error
	supSnapErr := &Supervisor{
		platform: PlatformSnap,
		name:     "vybench",
		output: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("snapctl failed")
		},
	}
	if _, err := supSnapErr.snapStatus(); err == nil {
		t.Error("expected error from snapStatus on output failure")
	}
}

func TestSetEnvAndFpmEnv(t *testing.T) {
	initial := []string{"FOO=bar", "HOME=/old/home", "BAZ=qux"}
	modified := setEnv(initial, "HOME", "/new/home")
	count := 0
	for _, e := range modified {
		if strings.HasPrefix(e, "HOME=") {
			count++
			if e != "HOME=/new/home" {
				t.Errorf("expected HOME=/new/home, got %s", e)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 HOME entry, got %d", count)
	}

	bEnv := benchEnv("/test/bench")
	benchCount := 0
	for _, e := range bEnv {
		if strings.HasPrefix(e, "BENCH_ROOT=") {
			benchCount++
			if e != "BENCH_ROOT=/test/bench" {
				t.Errorf("expected BENCH_ROOT=/test/bench, got %s", e)
			}
		}
	}
	if benchCount != 1 {
		t.Errorf("expected exactly 1 BENCH_ROOT entry, got %d", benchCount)
	}

	t.Setenv("SNAP", "/snap/vybench/current")
	t.Setenv("SNAP_COMMON", "/var/snap/vybench/common")
	fEnv := fpmEnv("/test/bench")
	homeCount := 0
	for _, e := range fEnv {
		if strings.HasPrefix(e, "HOME=") {
			homeCount++
			if e != "HOME=/var/snap/vybench/common" {
				t.Errorf("expected HOME=/var/snap/vybench/common, got %s", e)
			}
		}
	}
	if homeCount != 1 {
		t.Errorf("expected exactly 1 HOME entry in fpmEnv, got %d", homeCount)
	}
}

