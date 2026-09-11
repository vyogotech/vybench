// Package core provides the bench management engine for vybench multi-bench
// support: the benches.json registry, atomic current-bench symlink switching,
// site discovery, bench_id seeding (BC-11), and lazy migration from the
// single-bench layout.
package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Data types
// ──────────────────────────────────────────────────────────────────────────────

// BenchEntry is a single registered bench in benches.json.
type BenchEntry struct {
	Path          string `json:"path"`
	BenchID       string `json:"bench_id"`
	DBEngine      string `json:"db_engine"`      // "mariadb" or "postgres"
	FrappeVersion string `json:"frappe_version"` // "16.34.1", "develop", …
	CreatedAt     string `json:"created_at"`
}

// BenchRegistry is the top-level benches.json schema.
type BenchRegistry struct {
	ActiveBench string                `json:"active_bench"`
	Benches     map[string]BenchEntry `json:"benches"`
}

// BenchInfo is an enriched view of a registered bench.
type BenchInfo struct {
	Name     string
	Entry    BenchEntry
	Sites    []string
	IsActive bool // the registry's active bench, i.e. what services run
	Missing  bool // the directory no longer exists
}

// Where the active bench came from, in precedence order (BC-1).
const (
	SourceEnv     = "env"     // VYBENCH_BENCH (also set by --bench-path)
	SourceCwd     = "cwd"     // the current directory is a bench
	SourceSymlink = "symlink" // $VYBENCH_VAR/current-bench
	SourceLegacy  = "legacy"  // pre-multi-bench single bench
)

// ActiveBench is the bench a command operates on, and why it was chosen.
type ActiveBench struct {
	Name       string // registry name, or the directory name when unregistered
	Path       string
	Source     string
	Registered bool
	Entry      BenchEntry
}

// Pinned reports whether the bench was chosen by VYBENCH_BENCH or the current
// directory rather than the machine-wide current-bench symlink.
func (a ActiveBench) Pinned() bool { return a.Source == SourceEnv || a.Source == SourceCwd }

// PinReason explains a pinned bench, or returns "".
func (a ActiveBench) PinReason() string {
	switch a.Source {
	case SourceEnv:
		return "pinned by --bench-path / VYBENCH_BENCH"
	case SourceCwd:
		return "pinned by the current directory"
	}
	return ""
}

// ──────────────────────────────────────────────────────────────────────────────
// Manager
// ──────────────────────────────────────────────────────────────────────────────

// Manager is the core bench orchestration engine.
type Manager struct {
	// VarDir is $VYBENCH_VAR (Homebrew), $SNAP_COMMON (snap) or /var/vybench.
	// It holds benches/, the current-bench symlink and benches.json.
	VarDir string
	// ConfigDir is ~/.config/vybench, where earlier builds kept benches.json.
	ConfigDir string
	// PackagedPath is the read-only bench shipped with the package.
	PackagedPath string
}

// NewManager returns a Manager with paths detected from the environment.
func NewManager() *Manager {
	varDir := os.Getenv("VYBENCH_VAR")
	if varDir == "" {
		varDir = os.Getenv("SNAP_COMMON")
	}
	if varDir == "" {
		if runtime.GOOS == "darwin" {
			varDir = filepath.Join(BrewPrefix(), "var", InstanceName())
		} else {
			varDir = "/var/vybench"
		}
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return &Manager{
		VarDir:       varDir,
		ConfigDir:    filepath.Join(configDir, "vybench"),
		PackagedPath: PackagedBenchPath(),
	}
}

// RegistryPath is benches.json. It lives beside the current-bench symlink so
// every user, sudo, and the services see the same registry.
func (m *Manager) RegistryPath() string {
	return filepath.Join(m.VarDir, "benches.json")
}

func (m *Manager) legacyRegistryPath() string {
	return filepath.Join(m.ConfigDir, "benches.json")
}

func (m *Manager) registryExists() bool {
	return fileExists(m.RegistryPath()) || fileExists(m.legacyRegistryPath())
}

// LegacyBenchPath is the pre-multi-bench single bench location.
func (m *Manager) LegacyBenchPath() string {
	if runtime.GOOS != "darwin" && os.Getenv("SNAP_COMMON") == "" && isDir("/opt/frappe-bench") {
		return "/opt/frappe-bench"
	}
	return filepath.Join(m.VarDir, "bench")
}

// CurrentBenchSymlink is the symlink that points at the active bench.
func (m *Manager) CurrentBenchSymlink() string {
	return filepath.Join(m.VarDir, "current-bench")
}

// ──────────────────────────────────────────────────────────────────────────────
// Registry I/O
// ──────────────────────────────────────────────────────────────────────────────

// LoadRegistry reads benches.json, falling back to the old per-user location.
// Without either it synthesises a registry holding the legacy bench (BC-9).
func (m *Manager) LoadRegistry() (*BenchRegistry, error) {
	for _, p := range []string{m.RegistryPath(), m.legacyRegistryPath()} {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		var reg BenchRegistry
		if err := json.Unmarshal(data, &reg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", p, err)
		}
		if reg.Benches == nil {
			reg.Benches = make(map[string]BenchEntry)
		}
		return &reg, nil
	}
	return m.syntheticRegistry(), nil
}

// syntheticRegistry surfaces the legacy bench as "default" without touching
// any files.
func (m *Manager) syntheticRegistry() *BenchRegistry {
	legacy := m.LegacyBenchPath()
	benchID := readBenchIDFromConfig(legacy)
	if benchID == "" {
		benchID = "vybench-default"
	}
	ver := DetectFrappeVersion(legacy)
	if ver == "" {
		ver = m.DefaultFrappeVersion()
	}
	return &BenchRegistry{
		ActiveBench: "default",
		Benches: map[string]BenchEntry{
			"default": {
				Path:          legacy,
				BenchID:       benchID,
				DBEngine:      configDBEngine(legacy),
				FrappeVersion: ver,
			},
		},
	}
}

// SaveRegistry atomically writes benches.json.
func (m *Manager) SaveRegistry(reg *BenchRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(m.RegistryPath(), append(data, '\n'), 0o644)
}

func (r *BenchRegistry) names() []string {
	names := make([]string, 0, len(r.Benches))
	for n := range r.Benches {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (r *BenchRegistry) findByPath(path string) (string, BenchEntry, bool) {
	for _, name := range r.names() {
		if SamePath(r.Benches[name].Path, path) {
			return name, r.Benches[name], true
		}
	}
	return "", BenchEntry{}, false
}

// ──────────────────────────────────────────────────────────────────────────────
// Active bench resolution (BC-1)
// ──────────────────────────────────────────────────────────────────────────────

// IsBenchDir reports whether p looks like a bench (bench itself also requires
// config/ and logs/, which a freshly created bench may not have yet).
func IsBenchDir(p string) bool {
	return isDir(filepath.Join(p, "sites")) && isDir(filepath.Join(p, "apps"))
}

func (m *Manager) activePath() (string, string) {
	if p := os.Getenv("VYBENCH_BENCH"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p, SourceEnv
	}
	if cwd, err := os.Getwd(); err == nil && IsBenchDir(cwd) {
		return cwd, SourceCwd
	}
	if resolved, err := filepath.EvalSymlinks(m.CurrentBenchSymlink()); err == nil {
		return resolved, SourceSymlink
	}
	return m.LegacyBenchPath(), SourceLegacy
}

// ResolveActive returns the active bench using the precedence the shell
// wrappers use: VYBENCH_BENCH, the current directory, the current-bench
// symlink, then the legacy bench.
func (m *Manager) ResolveActive() ActiveBench {
	path, source := m.activePath()
	ab := ActiveBench{Path: path, Source: source}
	if reg, err := m.LoadRegistry(); err == nil {
		if name, e, ok := reg.findByPath(path); ok {
			ab.Name, ab.Entry, ab.Registered = name, e, true
		}
	}
	if !ab.Registered {
		ab.Name = filepath.Base(path)
		ab.Entry = BenchEntry{Path: path, DBEngine: configDBEngine(path)}
	}
	if v := DetectFrappeVersion(path); v != "" {
		ab.Entry.FrappeVersion = v
	}
	return ab
}

// ActiveBenchPath returns the resolved path of the active bench.
func (m *Manager) ActiveBenchPath() string {
	p, _ := m.activePath()
	return p
}

// ActiveBenchName returns the name of the active bench.
func (m *Manager) ActiveBenchName() string {
	return m.ResolveActive().Name
}

// BenchPaths lists every registered bench and the legacy bench, without
// duplicates.
func (m *Manager) BenchPaths() []string {
	paths := []string{m.LegacyBenchPath()}
	if reg, err := m.LoadRegistry(); err == nil {
		for _, name := range reg.names() {
			p := reg.Benches[name].Path
			if !slices.ContainsFunc(paths, func(q string) bool { return SamePath(p, q) }) {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

// NameFor returns the registry name of the bench at path, or its directory
// name when it is not registered.
func (m *Manager) NameFor(path string) string {
	if reg, err := m.LoadRegistry(); err == nil {
		if name, _, ok := reg.findByPath(path); ok {
			return name
		}
	}
	return filepath.Base(path)
}

// Bench returns a registered bench as an ActiveBench.
func (m *Manager) Bench(name string) (ActiveBench, error) {
	reg, err := m.LoadRegistry()
	if err != nil {
		return ActiveBench{}, err
	}
	e, ok := reg.Benches[name]
	if !ok {
		return ActiveBench{}, fmt.Errorf("bench %q not found — run 'vybench bench list' to see available benches", name)
	}
	if v := DetectFrappeVersion(e.Path); v != "" {
		e.FrappeVersion = v
	}
	return ActiveBench{Name: name, Path: e.Path, Source: SourceSymlink, Registered: true, Entry: e}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Switching, dropping, attaching
// ──────────────────────────────────────────────────────────────────────────────

// SwitchBench atomically repoints current-bench and records the active bench.
func (m *Manager) SwitchBench(name string) error {
	reg, err := m.LoadRegistry()
	if err != nil {
		return err
	}
	entry, ok := reg.Benches[name]
	if !ok {
		return fmt.Errorf("bench %q not found — run 'vybench bench list' to see available benches", name)
	}
	if !isDir(entry.Path) {
		return fmt.Errorf("bench %q points at %s, which no longer exists", name, entry.Path)
	}
	if !m.registryExists() {
		if err := m.migrateLegacy(reg); err != nil {
			return err
		}
	}
	if err := replaceSymlink(entry.Path, m.CurrentBenchSymlink()); err != nil {
		return err
	}
	reg.ActiveBench = name
	return m.SaveRegistry(reg)
}

// DropBench removes a bench registration. Files on disk are kept.
func (m *Manager) DropBench(name string) error {
	reg, err := m.LoadRegistry()
	if err != nil {
		return err
	}
	entry, ok := reg.Benches[name]
	if !ok {
		return fmt.Errorf("bench %q not found", name)
	}
	current, _ := filepath.EvalSymlinks(m.CurrentBenchSymlink())
	if reg.ActiveBench == name || (current != "" && SamePath(current, entry.Path)) {
		return fmt.Errorf("cannot drop active bench %q — switch to another bench first", name)
	}
	delete(reg.Benches, name)
	return m.SaveRegistry(reg)
}

// AttachBench registers an existing bench directory. dbEngine and
// frappeVersion are detected from the bench when empty. The bench's
// db_type is never rewritten.
func (m *Manager) AttachBench(name, path, dbEngine, frappeVersion string) error {
	if err := ValidateBenchName(name); err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path %q: %w", path, err)
	}
	if !isDir(filepath.Join(absPath, "sites")) {
		return fmt.Errorf("%s is not a bench (it has no sites/ directory)", absPath)
	}
	if dbEngine == "" {
		dbEngine = configDBEngine(absPath)
	} else if dbEngine, err = NormalizeDBEngine(dbEngine); err != nil {
		return err
	}
	reg, err := m.LoadRegistry()
	if err != nil {
		return err
	}
	if _, ok := reg.Benches[name]; ok {
		return fmt.Errorf("bench %q already exists", name)
	}
	if other, _, ok := reg.findByPath(absPath); ok {
		return fmt.Errorf("%s is already registered as %q", absPath, other)
	}
	if v := DetectFrappeVersion(absPath); v != "" {
		frappeVersion = v
	}
	benchID := readBenchIDFromConfig(absPath)
	if benchID == "" {
		benchID = "vybench-" + name
		if err := m.ensureBenchID(absPath, benchID); err != nil {
			return err
		}
	}
	if !m.registryExists() {
		if err := m.migrateLegacy(reg); err != nil {
			return err
		}
	}
	reg.Benches[name] = BenchEntry{
		Path:          absPath,
		BenchID:       benchID,
		DBEngine:      dbEngine,
		FrappeVersion: frappeVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	return m.SaveRegistry(reg)
}

// migrateLegacy runs before the first benches.json is written. A legacy
// single bench is kept as "default": its bench_id is pinned (BC-11) and
// current-bench is pointed at it so the services keep serving it. A machine
// with no legacy bench starts with an empty registry instead of a phantom one.
func (m *Manager) migrateLegacy(reg *BenchRegistry) error {
	legacy := m.LegacyBenchPath()
	if !isDir(filepath.Join(legacy, "sites")) {
		delete(reg.Benches, "default")
		if reg.ActiveBench == "default" {
			reg.ActiveBench = ""
		}
		return nil
	}
	if err := m.ensureBenchID(legacy, "vybench-default"); err != nil {
		return fmt.Errorf("pinning bench_id for %s: %w", legacy, err)
	}
	if e, ok := reg.Benches["default"]; ok {
		e.BenchID = readBenchIDFromConfig(legacy)
		reg.Benches["default"] = e
	}
	if _, err := os.Lstat(m.CurrentBenchSymlink()); errors.Is(err, fs.ErrNotExist) {
		if err := replaceSymlink(legacy, m.CurrentBenchSymlink()); err != nil {
			return err
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Bench creation (BC-11)
// ──────────────────────────────────────────────────────────────────────────────

// NewBenchOptions holds parameters for creating a new bench.
type NewBenchOptions struct {
	Name          string
	DBEngine      string // "mariadb" or "postgres"
	FrappeVersion string // "16", "15", "develop", "v16.3.0", a branch, or "" for the packaged release
	Python        string // --python for bench init; "" for the platform default
	SwitchToNew   bool
}

// BenchPlan is what CreateBench or InitBenchSpec will do for a set of options.
type BenchPlan struct {
	NewBenchOptions
	Path            string
	PackagedVersion string // Frappe release shipped with the package, "" if none
	NeedsInit       bool   // not the packaged release: must be built with bench init
	Branch          string // frappe branch or tag for bench init
}

// ErrNeedsInit is returned by CreateBench for a Frappe version other than the
// packaged release.
var ErrNeedsInit = errors.New("this Frappe version has to be built with bench init")

// PlanBench validates opts and decides how the bench will be built. The
// packaged Frappe release is linked in instantly; anything else needs
// `bench init`, which downloads Frappe and its dependencies.
func (m *Manager) PlanBench(opts NewBenchOptions) (BenchPlan, error) {
	if err := ValidateBenchName(opts.Name); err != nil {
		return BenchPlan{}, err
	}
	db, err := NormalizeDBEngine(opts.DBEngine)
	if err != nil {
		return BenchPlan{}, err
	}
	opts.DBEngine = db
	reg, err := m.LoadRegistry()
	if err != nil {
		return BenchPlan{}, err
	}
	if _, ok := reg.Benches[opts.Name]; ok {
		return BenchPlan{}, fmt.Errorf("bench %q already exists", opts.Name)
	}
	path := filepath.Join(m.VarDir, "benches", opts.Name)
	if fileExists(path) {
		return BenchPlan{}, fmt.Errorf("%s already exists — register it with 'vybench bench attach %s %s'", path, opts.Name, path)
	}

	packaged := m.PackagedFrappeVersion()
	plan := BenchPlan{NewBenchOptions: opts, Path: path, PackagedVersion: packaged}
	req := strings.TrimSpace(opts.FrappeVersion)
	if packaged != "" && (req == "" || SameRelease(req, packaged)) {
		plan.FrappeVersion = packaged
		return plan, nil
	}
	if req == "" {
		req = m.DefaultFrappeVersion()
	}
	plan.NeedsInit = true
	plan.FrappeVersion = req
	plan.Branch = FrappeBranch(req)
	return plan, nil
}

// CreateBench creates a bench on the packaged Frappe release. It returns
// ErrNeedsInit for any other version; use PlanBench and InitBenchSpec for those.
func (m *Manager) CreateBench(opts NewBenchOptions) error {
	plan, err := m.PlanBench(opts)
	if err != nil {
		return err
	}
	if plan.NeedsInit {
		return fmt.Errorf("%w: Frappe %q is not the packaged release (%s)", ErrNeedsInit, plan.FrappeVersion, orUnknown(plan.PackagedVersion))
	}
	return m.CreateLinked(plan)
}

// CreateLinked creates a bench whose apps/ and env/ are linked from the
// packaged bench. The wrappers create those links the first time a command
// runs against it; this seeds the layout and common_site_config.json so the
// bench_id is in place before any frappe process starts (BC-11).
func (m *Manager) CreateLinked(plan BenchPlan) error {
	for _, d := range []string{"sites", "logs", filepath.Join("config", "pids")} {
		if err := os.MkdirAll(filepath.Join(plan.Path, d), 0o775); err != nil {
			return fmt.Errorf("creating bench directory: %w", err)
		}
	}
	if err := m.writeBenchConfig(plan.Path, "vybench-"+plan.Name, plan.DBEngine, plan.FrappeVersion); err != nil {
		_ = os.RemoveAll(plan.Path)
		return fmt.Errorf("seeding common_site_config.json: %w", err)
	}
	if err := m.register(plan); err != nil {
		_ = os.RemoveAll(plan.Path)
		return err
	}
	return nil
}

// InitBenchSpec returns the job that builds plan with `bench init`, run
// through the wrapper of the bench at current, then registers it.
func (m *Manager) InitBenchSpec(plan BenchPlan, current string) (JobSpec, error) {
	parent := filepath.Dir(plan.Path)
	args := []string{"init", plan.Path, "--frappe-branch", plan.Branch,
		"--no-backups", "--skip-redis-config-generation"}
	py := plan.Python
	if py == "" {
		py = initPython()
	}
	if py != "" {
		args = append(args, "--python", py)
	}
	cmd, err := BenchCommand(current, args...)
	if err != nil {
		return JobSpec{}, err
	}
	cmd.Dir = parent

	built := false
	return JobSpec{
		Steps: []Step{
			{Label: "Preparing " + parent, Fn: func(func(string)) error {
				return prepareBenchesDir(parent)
			}},
			{Label: fmt.Sprintf("Building Frappe %s with bench init (downloads Frappe and its dependencies; takes several minutes)", plan.Branch), Cmd: cmd},
			{Label: "Registering bench " + plan.Name, Fn: func(log func(string)) error {
				built = true
				if v := DetectFrappeVersion(plan.Path); v != "" {
					plan.FrappeVersion = v
				}
				if err := m.writeBenchConfig(plan.Path, "vybench-"+plan.Name, plan.DBEngine, plan.FrappeVersion); err != nil {
					return fmt.Errorf("seeding common_site_config.json: %w", err)
				}
				if err := m.register(plan); err != nil {
					return err
				}
				log(fmt.Sprintf("Registered %s (Frappe %s, %s) at %s", plan.Name, FormatFrappeVersion(plan.FrappeVersion), plan.DBEngine, plan.Path))
				return nil
			}},
		},
		// Remove a half-built tree so the name can be reused. Once bench init
		// has succeeded the tree is kept, even if registering fails, so it can
		// be attached instead of rebuilt.
		OnFailure: func() {
			if !built {
				_ = os.RemoveAll(plan.Path)
			}
		},
	}, nil
}

func (m *Manager) register(plan BenchPlan) error {
	reg, err := m.LoadRegistry()
	if err != nil {
		return err
	}
	if !m.registryExists() {
		if err := m.migrateLegacy(reg); err != nil {
			return err
		}
	}
	if _, ok := reg.Benches[plan.Name]; ok {
		return fmt.Errorf("bench %q already exists", plan.Name)
	}
	reg.Benches[plan.Name] = BenchEntry{
		Path:          plan.Path,
		BenchID:       "vybench-" + plan.Name,
		DBEngine:      plan.DBEngine,
		FrappeVersion: plan.FrappeVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if plan.SwitchToNew {
		if err := replaceSymlink(plan.Path, m.CurrentBenchSymlink()); err != nil {
			return err
		}
		reg.ActiveBench = plan.Name
	}
	return m.SaveRegistry(reg)
}

// prepareBenchesDir makes the parent of new benches. Inside the snap, bench
// runs as snap_daemon even under sudo, so the directory must belong to it.
func prepareBenchesDir(dir string) error {
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return err
	}
	if DetectPlatform() != PlatformSnap || os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup("snap_daemon")
	if err != nil {
		return nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.Chown(dir, uid, gid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o2775)
}

var benchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

// ValidateBenchName rejects names that are unsafe as a directory name or a
// Redis namespace (bench_id is "vybench-<name>").
func ValidateBenchName(name string) error {
	if !benchNamePattern.MatchString(name) {
		return fmt.Errorf("invalid bench name %q: use 1-40 letters, digits, '-' or '_', starting with a letter or digit", name)
	}
	return nil
}

// NormalizeDBEngine maps user input to "mariadb" or "postgres".
func NormalizeDBEngine(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "mariadb", "maria", "mysql":
		return "mariadb", nil
	case "postgres", "postgresql", "pg":
		return "postgres", nil
	}
	return "", fmt.Errorf("unknown database engine %q: use mariadb or postgres", s)
}

var (
	majorOnly   = regexp.MustCompile(`^v?(\d+)$`)
	fullVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)
)

// FrappeBranch maps a version to the frappe git ref bench init clones:
// "15" → "version-15", "v16.3.0" → "v16.3.0", anything else unchanged.
func FrappeBranch(v string) string {
	v = strings.TrimSpace(v)
	if m := majorOnly.FindStringSubmatch(v); m != nil {
		return "version-" + m[1]
	}
	if fullVersion.MatchString(v) {
		return "v" + strings.TrimPrefix(v, "v")
	}
	return v
}

// SameRelease reports whether requested ("16", "v16", "version-16",
// "16.34.1") names the packaged release.
func SameRelease(requested, packaged string) bool {
	r := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(requested), "version-"), "v")
	p := strings.TrimPrefix(strings.TrimSpace(packaged), "v")
	if r == "" || p == "" {
		return false
	}
	if majorOnly.MatchString(r) {
		return r == MajorVersion(p)
	}
	return r == p
}

// MajorVersion returns the leading number of a version ("16.34.1" → "16").
func MajorVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		return v[:i]
	}
	return v
}

// ──────────────────────────────────────────────────────────────────────────────
// Site discovery
// ──────────────────────────────────────────────────────────────────────────────

// DiscoverSites returns the site names under <benchPath>/sites/, sorted.
func DiscoverSites(benchPath string) []string {
	sitesDir := filepath.Join(benchPath, "sites")
	entries, err := os.ReadDir(sitesDir)
	if err != nil {
		return nil
	}
	var sites []string
	for _, e := range entries {
		if e.Name() == "assets" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// A site directory must contain site_config.json. Stat follows
		// symlinks, so a symlinked site counts too.
		if fileExists(filepath.Join(sitesDir, e.Name(), "site_config.json")) {
			sites = append(sites, e.Name())
		}
	}
	return sites
}

// ListSiteDirs is DiscoverSites plus folders a failed `bench new-site` left
// behind before it wrote site_config.json: frappe creates a site's private/,
// public/ and locks/ first, and then refuses the name as taken. The Sites tab
// lists them so they can be dropped or replaced.
func ListSiteDirs(benchPath string) []string {
	sites := DiscoverSites(benchPath)
	entries, err := os.ReadDir(filepath.Join(benchPath, "sites"))
	if err != nil {
		return sites
	}
	for _, e := range entries {
		dir := filepath.Join(benchPath, "sites", e.Name())
		if !e.IsDir() || e.Name() == "assets" || strings.HasPrefix(e.Name(), ".") || fileExists(filepath.Join(dir, "site_config.json")) {
			continue
		}
		for _, sub := range []string{"private", "public", "locks"} {
			if isDir(filepath.Join(dir, sub)) {
				sites = append(sites, e.Name())
				break
			}
		}
	}
	sort.Strings(sites)
	return sites
}

// SiteDBType returns a site's db_type, falling back to the bench default.
func SiteDBType(benchPath, site string) string {
	if cfg, err := readConfig(filepath.Join(benchPath, "sites", site, "site_config.json")); err == nil {
		if t, ok := cfg["db_type"].(string); ok && t != "" {
			return t
		}
	}
	return configDBEngine(benchPath)
}

// WebserverPort returns the bench's webserver_port (default 8000).
func WebserverPort(benchPath string) int {
	cfg, err := readConfig(filepath.Join(benchPath, "sites", "common_site_config.json"))
	if err != nil {
		return 8000
	}
	switch v := cfg["webserver_port"].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8000
}

// ListBenches returns every registered bench, sorted by name.
func (m *Manager) ListBenches() ([]BenchInfo, error) {
	reg, err := m.LoadRegistry()
	if err != nil {
		return nil, err
	}
	var list []BenchInfo
	for _, name := range reg.names() {
		entry := reg.Benches[name]
		if detected := DetectFrappeVersion(entry.Path); detected != "" {
			entry.FrappeVersion = detected
		}
		list = append(list, BenchInfo{
			Name:     name,
			Entry:    entry,
			Sites:    DiscoverSites(entry.Path),
			IsActive: name == reg.ActiveBench,
			Missing:  !isDir(entry.Path),
		})
	}
	return list, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// common_site_config.json helpers (BC-11)
// ──────────────────────────────────────────────────────────────────────────────

func commonConfigPath(benchPath string) string {
	return filepath.Join(benchPath, "sites", "common_site_config.json")
}

// readConfig parses a JSON config, keeping numbers exact.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	return cfg, nil
}

// writeConfig writes in place, which keeps the file's owner and mode: on the
// snap it is shared with the services through its group.
func writeConfig(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// readBenchIDFromConfig reads "bench_id" from common_site_config.json.
func readBenchIDFromConfig(benchPath string) string {
	cfg, err := readConfig(commonConfigPath(benchPath))
	if err != nil {
		return ""
	}
	id, _ := cfg["bench_id"].(string)
	return id
}

func configDBEngine(benchPath string) string {
	if cfg, err := readConfig(commonConfigPath(benchPath)); err == nil {
		if t, ok := cfg["db_type"].(string); ok && t == "postgres" {
			return "postgres"
		}
	}
	return "mariadb"
}

// Keys that describe one bench and must not be copied from another.
var perBenchKeys = []string{"bench_id", "frappe_version", "db_type"}

// templateConfig returns the machine's common_site_config.json settings:
// Redis URLs, database socket and credentials, ports. The legacy bench's live
// file is preferred because the datastore wrappers write runtime values (the
// generated MariaDB root password, the socket path) into it.
func (m *Manager) templateConfig(includeLegacy bool) map[string]any {
	var candidates []string
	if includeLegacy {
		candidates = append(candidates, commonConfigPath(m.LegacyBenchPath()))
	}
	if etc := os.Getenv("VYBENCH_ETC"); etc != "" {
		candidates = append(candidates, filepath.Join(etc, "common_site_config.json"))
	}
	if snap := os.Getenv("SNAP"); snap != "" {
		candidates = append(candidates, filepath.Join(snap, "config", "common_site_config.json"))
	}
	for _, c := range candidates {
		if cfg, err := readConfig(c); err == nil {
			for _, k := range perBenchKeys {
				delete(cfg, k)
			}
			return cfg
		}
	}
	return map[string]any{}
}

// writeBenchConfig seeds a new bench's common_site_config.json. Machine
// settings from the template override whatever is there already, since the
// defaults `bench init` writes (Redis on 11000/13000, no socket) are not this
// machine's, then the bench's own keys are set.
func (m *Manager) writeBenchConfig(benchPath, benchID, dbEngine, version string) error {
	path := commonConfigPath(benchPath)
	cfg, err := readConfig(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg = make(map[string]any)
	} else if err != nil {
		return err
	}
	for k, v := range m.templateConfig(true) {
		cfg[k] = v
	}
	cfg["bench_id"] = benchID
	if version != "" {
		cfg["frappe_version"] = version
	}
	if dbEngine == "postgres" {
		cfg["db_type"] = "postgres"
		cfg["db_host"] = "127.0.0.1"
		cfg["db_port"] = 5432
		delete(cfg, "db_socket")
	} else {
		cfg["db_type"] = "mariadb"
	}
	return writeConfig(path, cfg)
}

// ensureBenchID adds bench_id to an existing bench without touching any other
// key. A bench with no common_site_config.json gets the install template, so
// the wrapper's "seed if missing" step is not defeated by a one-key file.
func (m *Manager) ensureBenchID(benchPath, benchID string) error {
	path := commonConfigPath(benchPath)
	cfg, err := readConfig(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = m.templateConfig(false)
	case err != nil:
		return err
	}
	if id, _ := cfg["bench_id"].(string); id != "" {
		return nil
	}
	cfg["bench_id"] = benchID
	return writeConfig(path, cfg)
}

// ──────────────────────────────────────────────────────────────────────────────
// Frappe versions
// ──────────────────────────────────────────────────────────────────────────────

// PackagedFrappeVersion is the Frappe release of the packaged bench, or "".
func (m *Manager) PackagedFrappeVersion() string {
	if m.PackagedPath == "" {
		return ""
	}
	return DetectFrappeVersion(m.PackagedPath)
}

// DefaultFrappeVersion is the version a new bench gets when none is given.
func (m *Manager) DefaultFrappeVersion() string {
	if v := os.Getenv("VYBENCH_DEFAULT_FRAPPE_VERSION"); v != "" {
		return v
	}
	if v := m.PackagedFrappeVersion(); v != "" {
		return v
	}
	if v := DetectFrappeVersion(m.LegacyBenchPath()); v != "" {
		return v
	}
	return "16"
}

// DetectFrappeVersion detects the Frappe version installed in a bench.
func DetectFrappeVersion(benchPath string) string {
	if benchPath == "" {
		return ""
	}

	// 1. apps/frappe/frappe/__init__.py
	if v := AppVersion(benchPath, "frappe"); v != "" {
		return v
	}

	// 2. apps/frappe git branch
	if data, err := os.ReadFile(filepath.Join(benchPath, "apps", "frappe", ".git", "HEAD")); err == nil {
		ref := strings.TrimSpace(string(data))
		if branch, ok := strings.CutPrefix(ref, "ref: refs/heads/"); ok {
			return strings.TrimPrefix(branch, "version-")
		}
	}

	// 3. common_site_config.json
	if cfg, err := readConfig(commonConfigPath(benchPath)); err == nil {
		if v, ok := cfg["frappe_version"].(string); ok && v != "" {
			return v
		}
	}

	// 4. python dist-info
	pattern := filepath.Join(benchPath, "env", "lib", "python*", "site-packages", "frappe-*.dist-info", "METADATA")
	if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
		if data, err := os.ReadFile(matches[0]); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "Version: "); ok {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	return ""
}

// AppVersion reads __version__ from apps/<app>/<app>/__init__.py.
func AppVersion(benchPath, app string) string {
	data, err := os.ReadFile(filepath.Join(benchPath, "apps", app, app, "__init__.py"))
	if err != nil {
		return ""
	}
	return extractPythonVersion(string(data))
}

func extractPythonVersion(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "__version__") {
			continue
		}
		if _, val, ok := strings.Cut(trimmed, "="); ok {
			if val = strings.Trim(strings.TrimSpace(val), `"' `); val != "" {
				return val
			}
		}
	}
	return ""
}

// FormatFrappeVersion formats a version for display ("16" → "v16").
func FormatFrappeVersion(v string) string {
	switch {
	case v == "":
		return "unknown"
	case strings.HasPrefix(v, "v"), v == "develop", !strings.ContainsAny(v[:1], "0123456789"):
		return v
	}
	return "v" + v
}

func orUnknown(s string) string {
	if s == "" {
		return "none found"
	}
	return s
}

// ──────────────────────────────────────────────────────────────────────────────
// Filesystem helpers
// ──────────────────────────────────────────────────────────────────────────────

// replaceSymlink atomically points link at target (ln -sfn, without the window
// in which link does not exist).
func replaceSymlink(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", link, err)
	}
	tmp := fmt.Sprintf("%s.tmp-%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("updating %s: %w", link, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SamePath reports whether a and b name the same directory once symlinks are resolved.
func SamePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}
