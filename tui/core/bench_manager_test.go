package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// newTestManager returns a Manager rooted in a temp dir, with a packaged bench
// on Frappe 16.34.1 and an install template, isolated from the real machine.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{"VYBENCH_BENCH", "SNAP", "SNAP_COMMON", "VYBENCH_DEFAULT_FRAPPE_VERSION"} {
		t.Setenv(k, "")
	}
	packaged := filepath.Join(tmp, "packaged")
	writeFile(t, filepath.Join(packaged, "apps", "frappe", "frappe", "__init__.py"), `__version__ = "16.34.1"`+"\n")

	fakeCLI(t)

	etc := filepath.Join(tmp, "etc")
	writeFile(t, filepath.Join(etc, "common_site_config.json"),
		`{"redis_cache": "redis://127.0.0.1:6379", "db_socket": "/tmp/mysql.sock", "webserver_port": 8000, "bench_id": "template-must-not-leak"}`)
	t.Setenv("VYBENCH_ETC", etc)

	return &Manager{
		VarDir:       filepath.Join(tmp, "var"),
		ConfigDir:    filepath.Join(tmp, "config"),
		PackagedPath: packaged,
	}
}

// fakeCLI points VYBENCH_CLI at a stub, so commands are built the same way
// whatever is installed on the machine running the tests.
func fakeCLI(t *testing.T) string {
	t.Helper()
	cli := filepath.Join(t.TempDir(), "vybench")
	writeFile(t, cli, "#!/bin/sh\n")
	if err := os.Chmod(cli, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VYBENCH_CLI", cli)
	return cli
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// makeLegacyBench creates the pre-multi-bench bench with the given config.
func makeLegacyBench(t *testing.T, m *Manager, config string) string {
	t.Helper()
	legacy := m.LegacyBenchPath()
	writeFile(t, filepath.Join(legacy, "sites", "common_site_config.json"), config)
	if err := os.MkdirAll(filepath.Join(legacy, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	return legacy
}

func TestSyntheticRegistry(t *testing.T) {
	m := newTestManager(t)
	reg, err := m.LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.ActiveBench != "default" {
		t.Errorf("active bench = %q, want default", reg.ActiveBench)
	}
	def, ok := reg.Benches["default"]
	if !ok {
		t.Fatal("synthetic registry has no default bench")
	}
	if def.Path != m.LegacyBenchPath() || def.BenchID != "vybench-default" || def.DBEngine != "mariadb" {
		t.Errorf("unexpected default entry %+v", def)
	}
	if fileExists(m.LegacyBenchPath()) {
		t.Error("loading the registry must not create the legacy bench")
	}
}

func TestSyntheticRegistryReadsLegacyDBType(t *testing.T) {
	m := newTestManager(t)
	makeLegacyBench(t, m, `{"db_type": "postgres"}`)
	reg, _ := m.LoadRegistry()
	if got := reg.Benches["default"].DBEngine; got != "postgres" {
		t.Errorf("DBEngine = %q, want postgres (from the bench's db_type)", got)
	}
}

func TestDetectFrappeVersion(t *testing.T) {
	bench := t.TempDir()
	initPy := filepath.Join(bench, "apps", "frappe", "frappe", "__init__.py")

	if v := DetectFrappeVersion(bench); v != "" {
		t.Errorf("empty bench: got %q", v)
	}

	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"frappe_version": "15"}`)
	if v := DetectFrappeVersion(bench); v != "15" {
		t.Errorf("from common_site_config: got %q, want 15", v)
	}

	writeFile(t, filepath.Join(bench, "apps", "frappe", ".git", "HEAD"), "ref: refs/heads/version-16\n")
	if v := DetectFrappeVersion(bench); v != "16" {
		t.Errorf("from git branch: got %q, want 16", v)
	}

	writeFile(t, initPy, "# Frappe\n__version__ = \"15.63.3\"\n")
	if v := DetectFrappeVersion(bench); v != "15.63.3" {
		t.Errorf("from __init__.py: got %q", v)
	}
	writeFile(t, initPy, `__version__ = "17.0.0-dev"`)
	if v := DetectFrappeVersion(bench); v != "17.0.0-dev" {
		t.Errorf("dev version: got %q", v)
	}
}

func TestFormatFrappeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"": "unknown", "16": "v16", "15.63.3": "v15.63.3", "17.0.0-dev": "v17.0.0-dev",
		"v16": "v16", "develop": "develop", "main": "main",
	} {
		if got := FormatFrappeVersion(in); got != want {
			t.Errorf("FormatFrappeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFrappeBranchAndSameRelease(t *testing.T) {
	branches := map[string]string{
		"15": "version-15", "v16": "version-16", "version-15": "version-15",
		"16.0.0": "v16.0.0", "v16.3.1": "v16.3.1", "develop": "develop", "my-fork-branch": "my-fork-branch",
	}
	for in, want := range branches {
		if got := FrappeBranch(in); got != want {
			t.Errorf("FrappeBranch(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		req  string
		want bool
	}{
		{"16", true}, {"v16", true}, {"version-16", true}, {"16.34.1", true}, {"v16.34.1", true},
		{"15", false}, {"16.0.0", false}, {"develop", false}, {"", false},
	} {
		if got := SameRelease(c.req, "16.34.1"); got != c.want {
			t.Errorf("SameRelease(%q, 16.34.1) = %v, want %v", c.req, got, c.want)
		}
	}
}

func TestValidateBenchName(t *testing.T) {
	for _, ok := range []string{"a", "erp-v15", "Bench_2", strings.Repeat("x", 40)} {
		if err := ValidateBenchName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../etc", "a/b", ".hidden", "-flag", "has space", strings.Repeat("x", 41)} {
		if ValidateBenchName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPlanBench(t *testing.T) {
	m := newTestManager(t)
	for _, c := range []struct {
		version   string
		needsInit bool
		branch    string
		recorded  string
	}{
		{"", false, "", "16.34.1"},
		{"16", false, "", "16.34.1"},
		{"version-16", false, "", "16.34.1"},
		{"16.34.1", false, "", "16.34.1"},
		{"15", true, "version-15", "15"},
		{"develop", true, "develop", "develop"},
		{"v16.0.0", true, "v16.0.0", "v16.0.0"},
	} {
		plan, err := m.PlanBench(NewBenchOptions{Name: "b", FrappeVersion: c.version})
		if err != nil {
			t.Fatalf("PlanBench(%q): %v", c.version, err)
		}
		if plan.NeedsInit != c.needsInit || plan.Branch != c.branch || plan.FrappeVersion != c.recorded {
			t.Errorf("PlanBench(%q) = init %v branch %q version %q; want %v %q %q",
				c.version, plan.NeedsInit, plan.Branch, plan.FrappeVersion, c.needsInit, c.branch, c.recorded)
		}
		if plan.Path != filepath.Join(m.VarDir, "benches", "b") {
			t.Errorf("path = %s", plan.Path)
		}
	}

	if _, err := m.PlanBench(NewBenchOptions{Name: "../escape"}); err == nil {
		t.Error("path traversal in the bench name was accepted")
	}
	if _, err := m.PlanBench(NewBenchOptions{Name: "b", DBEngine: "oracle"}); err == nil {
		t.Error("unknown database engine was accepted")
	}
	if err := os.MkdirAll(filepath.Join(m.VarDir, "benches", "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PlanBench(NewBenchOptions{Name: "taken"}); err == nil || !strings.Contains(err.Error(), "attach") {
		t.Errorf("existing directory: err = %v, want a hint to attach it", err)
	}
}

func TestPlanBenchWithoutPackagedBenchNeedsInit(t *testing.T) {
	m := newTestManager(t)
	m.PackagedPath = ""
	plan, err := m.PlanBench(NewBenchOptions{Name: "b", FrappeVersion: "16"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NeedsInit || plan.Branch != "version-16" {
		t.Errorf("with nothing to link, a bench must be built: %+v", plan)
	}
}

func TestCreateBenchRejectsOtherReleases(t *testing.T) {
	m := newTestManager(t)
	err := m.CreateBench(NewBenchOptions{Name: "old", FrappeVersion: "15"})
	if !errors.Is(err, ErrNeedsInit) {
		t.Fatalf("err = %v, want ErrNeedsInit", err)
	}
	if fileExists(filepath.Join(m.VarDir, "benches", "old")) {
		t.Error("a rejected bench left a directory behind")
	}
}

func TestCreateLinkedBenches(t *testing.T) {
	m := newTestManager(t)

	if err := m.CreateBench(NewBenchOptions{Name: "zeta", DBEngine: "mariadb", SwitchToNew: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateBench(NewBenchOptions{Name: "alpha", DBEngine: "postgres", FrappeVersion: "16"}); err != nil {
		t.Fatal(err)
	}

	benches, err := m.ListBenches()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range benches {
		names = append(names, b.Name)
	}
	if !slices.Equal(names, []string{"alpha", "zeta"}) {
		t.Errorf("ListBenches order = %v, want sorted", names)
	}
	if !benches[1].IsActive || benches[0].IsActive {
		t.Error("only zeta (created with SwitchToNew) should be active")
	}
	if benches[1].Entry.FrappeVersion != "16.34.1" {
		t.Errorf("zeta version = %q, want the packaged release", benches[1].Entry.FrappeVersion)
	}

	// The seeded config carries the machine's settings, not just bench_id, so
	// the wrapper's "copy the template if missing" step is not defeated.
	zeta := readJSON(t, filepath.Join(m.VarDir, "benches", "zeta", "sites", "common_site_config.json"))
	if zeta["redis_cache"] != "redis://127.0.0.1:6379" || zeta["db_socket"] != "/tmp/mysql.sock" {
		t.Errorf("zeta config lost the install template: %v", zeta)
	}
	if zeta["bench_id"] != "vybench-zeta" || zeta["db_type"] != "mariadb" || zeta["frappe_version"] != "16.34.1" {
		t.Errorf("zeta bench keys: %v", zeta)
	}
	for _, d := range []string{"logs", "config/pids"} {
		if !isDir(filepath.Join(m.VarDir, "benches", "zeta", d)) {
			t.Errorf("missing %s/", d)
		}
	}

	alpha := readJSON(t, filepath.Join(m.VarDir, "benches", "alpha", "sites", "common_site_config.json"))
	if alpha["db_type"] != "postgres" || alpha["db_host"] != "127.0.0.1" || alpha["db_socket"] != nil {
		t.Errorf("alpha postgres keys: %v", alpha)
	}
	if port, _ := alpha["db_port"].(json.Number); port.String() != "5432" {
		t.Errorf("alpha db_port = %v", alpha["db_port"])
	}

	if got := m.ActiveBenchName(); got != "zeta" {
		t.Errorf("active = %q, want zeta", got)
	}
	if err := m.SwitchBench("alpha"); err != nil {
		t.Fatal(err)
	}
	active := m.ResolveActive()
	if active.Name != "alpha" || active.Source != SourceSymlink || !active.Registered || active.Pinned() {
		t.Errorf("after switch: %+v", active)
	}
	if err := m.DropBench("alpha"); err == nil {
		t.Error("dropping the active bench succeeded")
	}
	if err := m.DropBench("zeta"); err != nil {
		t.Fatal(err)
	}
	if !isDir(filepath.Join(m.VarDir, "benches", "zeta")) {
		t.Error("drop deleted the bench's files")
	}
}

// The first multi-bench action on a machine with no legacy bench must not
// invent one.
func TestMigrationWithoutLegacyBench(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateBench(NewBenchOptions{Name: "first"}); err != nil {
		t.Fatal(err)
	}
	reg, _ := m.LoadRegistry()
	if _, ok := reg.Benches["default"]; ok {
		t.Error("registry has a phantom default bench")
	}
	if fileExists(m.LegacyBenchPath()) {
		t.Error("migration created the legacy bench directory")
	}
	if _, err := os.Lstat(m.CurrentBenchSymlink()); err == nil {
		t.Error("migration pointed current-bench at a bench that does not exist")
	}
}

// Migration pins bench_id on the legacy bench without touching its other
// settings (a PostgreSQL legacy bench must stay PostgreSQL).
func TestMigrationPreservesLegacyConfig(t *testing.T) {
	m := newTestManager(t)
	legacy := makeLegacyBench(t, m, `{"db_type": "postgres", "root_password": "s3cret", "redis_queue": "redis://127.0.0.1:11311"}`)
	if err := m.CreateBench(NewBenchOptions{Name: "second"}); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(legacy, "sites", "common_site_config.json"))
	if cfg["db_type"] != "postgres" || cfg["root_password"] != "s3cret" || cfg["bench_id"] != "vybench-default" {
		t.Errorf("legacy config after migration: %v", cfg)
	}
	target, err := filepath.EvalSymlinks(m.CurrentBenchSymlink())
	if err != nil || !SamePath(target, legacy) {
		t.Errorf("current-bench = %q (%v), want the legacy bench", target, err)
	}
	// New benches inherit the legacy bench's runtime settings.
	second := readJSON(t, filepath.Join(m.VarDir, "benches", "second", "sites", "common_site_config.json"))
	if second["root_password"] != "s3cret" || second["db_type"] != "mariadb" || second["bench_id"] != "vybench-second" {
		t.Errorf("new bench config: %v", second)
	}
}

func TestRegistryMovesFromConfigDir(t *testing.T) {
	m := newTestManager(t)
	bench := filepath.Join(t.TempDir(), "old")
	if err := os.MkdirAll(bench, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, m.legacyRegistryPath(), `{"active_bench": "old", "benches": {"old": {"path": "`+bench+`"}}}`)
	reg, err := m.LoadRegistry()
	if err != nil || reg.ActiveBench != "old" {
		t.Fatalf("legacy registry not read: %v %+v", err, reg)
	}
	if err := m.SwitchBench("old"); err != nil {
		t.Fatal(err)
	}
	if !fileExists(m.RegistryPath()) {
		t.Error("saving did not write benches.json beside current-bench")
	}
}

func TestResolveActivePrecedence(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateBench(NewBenchOptions{Name: "linked", SwitchToNew: true}); err != nil {
		t.Fatal(err)
	}
	if a := m.ResolveActive(); a.Source != SourceSymlink || a.Name != "linked" {
		t.Errorf("symlink: %+v", a)
	}

	other := t.TempDir()
	for _, d := range []string{"sites", "apps"} {
		if err := os.MkdirAll(filepath.Join(other, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(other)
	a := m.ResolveActive()
	if a.Source != SourceCwd || a.Registered || !a.Pinned() || a.Name != filepath.Base(other) {
		t.Errorf("cwd: %+v", a)
	}

	t.Setenv("VYBENCH_BENCH", filepath.Join(m.VarDir, "benches", "linked"))
	a = m.ResolveActive()
	if a.Source != SourceEnv || a.Name != "linked" || !a.Registered || a.PinReason() == "" {
		t.Errorf("env: %+v", a)
	}
}

func TestSwitchBenchToMissingDirectory(t *testing.T) {
	m := newTestManager(t)
	if err := m.CreateBench(NewBenchOptions{Name: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(m.VarDir, "benches", "gone")); err != nil {
		t.Fatal(err)
	}
	if err := m.SwitchBench("gone"); err == nil {
		t.Error("switched to a bench whose directory is gone")
	}
	benches, _ := m.ListBenches()
	if len(benches) != 1 || !benches[0].Missing {
		t.Errorf("missing bench not flagged: %+v", benches)
	}
}

func TestAttachBench(t *testing.T) {
	m := newTestManager(t)
	ext := filepath.Join(t.TempDir(), "external")
	writeFile(t, filepath.Join(ext, "sites", "common_site_config.json"), `{"db_type": "postgres", "db_port": 5433}`)
	writeFile(t, filepath.Join(ext, "apps", "frappe", "frappe", "__init__.py"), `__version__ = "15.80.0"`)

	if err := m.AttachBench("ext", ext, "", ""); err != nil {
		t.Fatal(err)
	}
	reg, _ := m.LoadRegistry()
	e := reg.Benches["ext"]
	if e.DBEngine != "postgres" || e.FrappeVersion != "15.80.0" || e.BenchID != "vybench-ext" {
		t.Errorf("attached entry: %+v", e)
	}
	cfg := readJSON(t, filepath.Join(ext, "sites", "common_site_config.json"))
	if cfg["db_type"] != "postgres" || cfg["bench_id"] != "vybench-ext" {
		t.Errorf("attach changed the bench config: %v", cfg)
	}
	if port, _ := cfg["db_port"].(json.Number); port.String() != "5433" {
		t.Errorf("attach rewrote db_port: %v", cfg["db_port"])
	}
	if err := m.AttachBench("again", ext, "", ""); err == nil {
		t.Error("attached the same directory twice")
	}
	if err := m.AttachBench("nobench", t.TempDir(), "", ""); err == nil {
		t.Error("attached a directory without sites/")
	}
}

func TestInitBenchSpec(t *testing.T) {
	m := newTestManager(t)
	plan, err := m.PlanBench(NewBenchOptions{Name: "v15", FrappeVersion: "15", Python: "/usr/bin/python3.12"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := m.InitBenchSpec(plan, filepath.Join(m.VarDir, "bench"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps) != 3 || spec.Steps[1].Cmd == nil {
		t.Fatalf("unexpected steps: %+v", spec.Steps)
	}
	args := spec.Steps[1].Cmd.Args
	i := slices.Index(args, "init")
	if i < 0 || !slices.Equal(args[i:], []string{"init", plan.Path, "--frappe-branch", "version-15",
		"--no-backups", "--skip-redis-config-generation", "--python", "/usr/bin/python3.12"}) {
		t.Errorf("bench init args = %v", args)
	}
	if spec.Steps[1].Cmd.Dir != filepath.Dir(plan.Path) {
		t.Errorf("bench init runs in %s, want the benches/ directory", spec.Steps[1].Cmd.Dir)
	}

	if err := spec.Steps[0].Fn(func(string) {}); err != nil {
		t.Fatal(err)
	}
	// A failed bench init leaves a partial tree; it must be removed.
	writeFile(t, filepath.Join(plan.Path, "apps", "frappe", "README"), "partial clone")
	spec.OnFailure()
	if fileExists(plan.Path) {
		t.Error("OnFailure kept a half-built bench")
	}

	// Simulate a successful bench init, then run the registration step.
	writeFile(t, filepath.Join(plan.Path, "apps", "frappe", "frappe", "__init__.py"), `__version__ = "15.80.0"`)
	writeFile(t, filepath.Join(plan.Path, "sites", "common_site_config.json"),
		`{"redis_cache": "redis://127.0.0.1:13000", "socketio_port": 9000}`)
	var logged []string
	if err := spec.Steps[2].Fn(func(l string) { logged = append(logged, l) }); err != nil {
		t.Fatal(err)
	}
	reg, _ := m.LoadRegistry()
	if e := reg.Benches["v15"]; e.FrappeVersion != "15.80.0" || e.BenchID != "vybench-v15" {
		t.Errorf("registered entry: %+v", e)
	}
	cfg := readJSON(t, filepath.Join(plan.Path, "sites", "common_site_config.json"))
	if cfg["redis_cache"] != "redis://127.0.0.1:6379" {
		t.Errorf("bench init's default Redis URL was kept: %v", cfg["redis_cache"])
	}
	if cfg["socketio_port"] == nil || cfg["bench_id"] != "vybench-v15" {
		t.Errorf("registration config: %v", cfg)
	}
	spec.OnFailure()
	if !fileExists(plan.Path) {
		t.Error("OnFailure removed a bench that bench init had finished")
	}
	if len(logged) == 0 {
		t.Error("registration step logged nothing")
	}
}

func TestSiteHelpers(t *testing.T) {
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"webserver_port": 8123}`)
	writeFile(t, filepath.Join(bench, "sites", "b.localhost", "site_config.json"), `{"db_type": "postgres"}`)
	writeFile(t, filepath.Join(bench, "sites", "a.localhost", "site_config.json"), `{}`)
	writeFile(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\n")
	if err := os.MkdirAll(filepath.Join(bench, "sites", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := DiscoverSites(bench); !slices.Equal(got, []string{"a.localhost", "b.localhost"}) {
		t.Errorf("DiscoverSites = %v", got)
	}
	if got := SiteDBType(bench, "b.localhost"); got != "postgres" {
		t.Errorf("SiteDBType(b) = %q", got)
	}
	if got := SiteDBType(bench, "a.localhost"); got != "mariadb" {
		t.Errorf("SiteDBType(a) = %q", got)
	}
	if got := SiteURL(bench, "a.localhost"); got != "http://a.localhost:8123" {
		t.Errorf("SiteURL = %q", got)
	}
	if got := WebserverPort(t.TempDir()); got != 8000 {
		t.Errorf("default port = %d", got)
	}
}

// Regression: with no vybench command and no bench in the bench's env, the
// job used to start and fail with a bare "fork/exec … no such file".
func TestBenchCommandExplainsMissingCLI(t *testing.T) {
	for _, k := range []string{"VYBENCH_CLI", "SNAP", "VYBENCH_LIBEXEC"} {
		t.Setenv(k, "")
	}
	t.Setenv("VYBENCH_NAME", "vybench")
	t.Setenv("HOMEBREW_PREFIX", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	bench := t.TempDir()
	if _, err := BenchCommand(bench, "--version"); err == nil || !strings.Contains(err.Error(), "no bench command found") {
		t.Errorf("err = %v", err)
	}
	m := newTestManager(t)
	t.Setenv("VYBENCH_CLI", "")
	plan, _ := m.PlanBench(NewBenchOptions{Name: "x", FrappeVersion: "15"})
	if _, err := m.InitBenchSpec(plan, bench); err == nil {
		t.Error("InitBenchSpec started without a bench command")
	}
	if fileExists(filepath.Dir(plan.Path)) {
		t.Error("benches/ was created before the command was known to exist")
	}

	// A bench of its own, or frappe-bench on PATH, is enough.
	own := filepath.Join(bench, "env", "bin", "bench")
	writeFile(t, own, "#!/bin/sh\n")
	_ = os.Chmod(own, 0o755)
	if cmd, err := BenchCommand(bench, "--version"); err != nil || cmd.Path != own {
		t.Errorf("own bench: %v %v", cmd, err)
	}
}

func TestInstanceNameFindsLocalFormula(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Homebrew instance detection is macOS-only")
	}
	t.Setenv("VYBENCH_NAME", "")
	t.Setenv("SNAP_INSTANCE_NAME", "")
	prefix := t.TempDir()
	t.Setenv("HOMEBREW_PREFIX", prefix)
	if got := InstanceName(); got != "vybench" {
		t.Errorf("nothing installed: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "opt", "vybench-local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := InstanceName(); got != "vybench-local" {
		t.Errorf("only vybench-local installed: %q", got)
	}
	cli := filepath.Join(prefix, "opt", "vybench-local", "bin", "vybench")
	writeFile(t, cli, "#!/bin/sh\n")
	_ = os.Chmod(cli, 0o755)
	if got := vybenchCLI(); got != cli {
		t.Errorf("unlinked keg's vybench not found: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "opt", "vybench"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := InstanceName(); got != "vybench" {
		t.Errorf("both installed: %q, want vybench", got)
	}
}
