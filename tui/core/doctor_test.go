package core

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeBench builds a bench that passes every layout check, so a test can break
// exactly one thing and see only that.
func fakeBench(t *testing.T, apps ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"sites", "config/pids", "logs", "apps", "env/lib/python3.14/site-packages"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o775); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	write("sites/common_site_config.json", `{"db_type":"mariadb"}`)
	write("sites/apps.txt", strings.Join(apps, "\n")+"\n")
	for _, a := range apps {
		dir := filepath.Join(root, "apps", a, a)
		if err := os.MkdirAll(dir, 0o775); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("apps", a, a, "__init__.py"), "")
		write(filepath.Join("env/lib/python3.14/site-packages", a+".pth"), filepath.Join(root, "apps", a)+"\n")
	}
	return root
}

func findingsFor(r Report, check string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Check == check && f.Severity != SevOK {
			out = append(out, f)
		}
	}
	return out
}

func hasTitle(r Report, substr string) bool {
	for _, f := range r.Findings {
		if strings.Contains(f.Title, substr) {
			return true
		}
	}
	return false
}

func TestDoctorHealthyBench(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe", "erpnext")
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	for _, f := range r.Findings {
		if f.Check == "apps" && f.Severity != SevOK {
			t.Fatalf("unexpected app finding on a sound bench: %s — %s", f.Title, f.Detail)
		}
	}
	if !hasTitle(r, "bench layout is complete") {
		t.Fatalf("layout not reported complete: %+v", r.Findings)
	}
}

func TestDoctorMissingLayoutDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe")
	if err := os.RemoveAll(filepath.Join(bench, "config", "pids")); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "config/pids") {
		t.Fatalf("missing config/pids not reported: %+v", r.Findings)
	}
	if fixed, failed := r.Apply(); fixed == 0 || failed > 0 {
		t.Fatalf("apply: fixed=%d failed=%d", fixed, failed)
	}
	if !isDir(filepath.Join(bench, "config", "pids")) {
		t.Fatal("config/pids was not recreated")
	}
}

// A bench whose apps/ is gone must be reported as one broken directory, never
// as a pile of stale registrations -- the repair for the latter deletes the
// record of everything that was installed.
func TestDoctorMissingAppsDirDoesNotEraseAppsTxt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe", "erpnext")
	if err := os.RemoveAll(filepath.Join(bench, "apps")); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if hasTitle(r, "registered but not installed") {
		t.Fatal("a missing apps/ was reported as stale registrations")
	}
	r.Apply()
	got, err := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"frappe", "erpnext"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("apps.txt lost %q: %q", want, got)
		}
	}
}

func TestDoctorStaleRegistrationRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe", "ghost")
	if err := os.RemoveAll(filepath.Join(bench, "apps", "ghost")); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "registered but not installed: ghost") {
		t.Fatalf("stale registration not reported: %+v", findingsFor(r, "apps"))
	}
	r.Apply()
	got, _ := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if strings.Contains(string(got), "ghost") {
		t.Fatalf("ghost still registered: %q", got)
	}
	if !strings.Contains(string(got), "frappe") {
		t.Fatalf("frappe was removed too: %q", got)
	}
}

func TestDoctorUnlistedAppIsRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe")
	pkg := filepath.Join(bench, "apps", "wiki", "wiki")
	if err := os.MkdirAll(pkg, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "__init__.py"), nil, 0o664); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "installed but not registered: wiki") {
		t.Fatalf("unlisted app not reported: %+v", findingsFor(r, "apps"))
	}
	r.Apply()
	got, _ := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if !strings.Contains(string(got), "wiki") {
		t.Fatalf("wiki was not registered: %q", got)
	}
	if !strings.HasPrefix(string(got), "frappe") {
		t.Fatalf("frappe must stay first: %q", got)
	}
}

// The failure the whole file exists for: an .pth left behind by an install
// made under a different HOME.
func TestDoctorRepointsStalePTH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe", "payments")
	pth := filepath.Join(bench, "env", "lib", "python3.14", "site-packages", "payments.pth")
	if err := os.WriteFile(pth, []byte("/root/.fpm/apps/frappe/payments/0.0.0\n"), 0o664); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "payments.pth points at the wrong directory") {
		t.Fatalf("stale .pth not reported: %+v", findingsFor(r, "apps"))
	}
	if fixed, failed := r.Apply(); fixed == 0 || failed > 0 {
		t.Fatalf("apply: fixed=%d failed=%d", fixed, failed)
	}
	got, _ := os.ReadFile(pth)
	// Compared with samePath: the repair writes the resolved path, and on macOS
	// a temp directory reaches it through /var -> /private/var.
	want := filepath.Join(bench, "apps", "payments")
	if !samePath(strings.TrimSpace(string(got)), want) {
		t.Fatalf("pth = %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

func TestDoctorWritesMissingPTH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe", "wiki")
	pth := filepath.Join(bench, "env", "lib", "python3.14", "site-packages", "wiki.pth")
	if err := os.Remove(pth); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "wiki is not on the virtualenv's path") {
		t.Fatalf("missing .pth not reported: %+v", findingsFor(r, "apps"))
	}
	r.Apply()
	if _, err := os.Stat(pth); err != nil {
		t.Fatalf("pth not written: %v", err)
	}
}

func TestDoctorSeedsFPMRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bench := fakeBench(t, "frappe")
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "fpm has no repository configured") {
		t.Fatalf("missing fpm config not reported: %+v", findingsFor(r, "fpm"))
	}
	if fixed, failed := r.Apply(); fixed == 0 || failed > 0 {
		t.Fatalf("apply: fixed=%d failed=%d", fixed, failed)
	}
	data, err := os.ReadFile(filepath.Join(home, ".fpm", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Repositories map[string]struct{ URL string } `json:"repositories"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Repositories["vyogo"].URL != DefaultRegistryURL {
		t.Fatalf("repository = %+v", cfg.Repositories)
	}
}

func TestDoctorArchivesSiteWithoutDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe")
	site := filepath.Join(bench, "sites", "dead.localhost")
	if err := os.MkdirAll(site, 0o775); err != nil {
		t.Fatal(err)
	}
	// No db_name: frappe writes one before it creates the database, so a site
	// without one never got that far.
	if err := os.WriteFile(filepath.Join(site, "site_config.json"), []byte(`{}`), 0o664); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if !hasTitle(r, "dead.localhost is left over") {
		t.Fatalf("incomplete site not reported: %+v", findingsFor(r, "sites"))
	}
	if fixed, failed := r.Apply(); fixed == 0 || failed > 0 {
		t.Fatalf("apply: fixed=%d failed=%d", fixed, failed)
	}
	if fileExists(site) {
		t.Fatal("the site directory is still in sites/")
	}
	entries, err := os.ReadDir(filepath.Join(bench, "archived", "sites"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("archived/sites = %v, %v", entries, err)
	}
}

func TestDoctorLeavesHealthySiteAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bench := fakeBench(t, "frappe")
	site := filepath.Join(bench, "sites", "live.localhost")
	if err := os.MkdirAll(site, 0o775); err != nil {
		t.Fatal(err)
	}
	cfg := `{"db_name":"_abc","installed_apps":["frappe"]}`
	if err := os.WriteFile(filepath.Join(site, "site_config.json"), []byte(cfg), 0o664); err != nil {
		t.Fatal(err)
	}
	r := RunDoctor(context.Background(), bench, DoctorOptions{SkipNetwork: true})
	if hasTitle(r, "live.localhost is left over") {
		t.Fatal("a working site was reported as incomplete")
	}
	r.Apply()
	if !fileExists(site) {
		t.Fatal("a working site was archived")
	}
}

func TestReportSeverity(t *testing.T) {
	r := Report{Findings: []Finding{{Severity: SevOK}, {Severity: SevWarn}}}
	if r.Worst() != SevWarn || r.Healthy() {
		t.Fatalf("worst=%v healthy=%v", r.Worst(), r.Healthy())
	}
	if len(r.Problems()) != 1 {
		t.Fatalf("problems = %d", len(r.Problems()))
	}
	r.Findings = append(r.Findings, Finding{Severity: SevError})
	if r.Worst() != SevError {
		t.Fatalf("worst = %v", r.Worst())
	}
	if SevError.String() != "FAIL" || SevWarn.String() != "WARN" || SevOK.String() != "OK" {
		t.Fatal("severity names changed")
	}
}

func TestReportApplyRecordsFailure(t *testing.T) {
	r := Report{Findings: []Finding{
		{Severity: SevError, fix: func() error { return os.ErrPermission }},
		{Severity: SevError, fix: func() error { return nil }},
		{Severity: SevOK, fix: func() error { t.Fatal("an OK finding was repaired"); return nil }},
	}}
	fixed, failed := r.Apply()
	if fixed != 1 || failed != 1 {
		t.Fatalf("fixed=%d failed=%d", fixed, failed)
	}
	if r.Findings[0].FixErr == nil || !r.Findings[1].Fixed {
		t.Fatalf("outcomes not recorded: %+v", r.Findings)
	}
}

func TestPackageDirLayouts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "app"), 0o775); err != nil {
		t.Fatal(err)
	}
	if _, ok := packageDir(root, "app"); ok {
		t.Fatal("a directory with no __init__.py counted as a package")
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app", "__init__.py"), nil, 0o664); err != nil {
		t.Fatal(err)
	}
	got, ok := packageDir(root, "app")
	if !ok || got != filepath.Join(root, "src", "app") {
		t.Fatalf("packageDir = %q, %v", got, ok)
	}
}

func TestReadPTHIgnoresImportLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.pth")
	if err := os.WriteFile(p, []byte("import sys\n# a comment\n/opt/app\n"), 0o664); err != nil {
		t.Fatal(err)
	}
	got, ok := readPTH(p)
	if !ok || got != "/opt/app" {
		t.Fatalf("readPTH = %q, %v", got, ok)
	}
	if _, ok := readPTH(filepath.Join(dir, "absent.pth")); ok {
		t.Fatal("a missing .pth reported as present")
	}
}

func TestWriteAppsTxtKeepsFrappeFirstAndDedupes(t *testing.T) {
	bench := fakeBench(t)
	if err := writeAppsTxt(bench, []string{"wiki", "frappe", "wiki", "", "erpnext"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if string(got) != "frappe\nwiki\nerpnext\n" {
		t.Fatalf("apps.txt = %q", got)
	}
}

func TestSamePathFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o775); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !samePath(link, real) || !samePath(real+"/", real) {
		t.Fatal("samePath did not resolve equivalent paths")
	}
	if samePath(real, dir) {
		t.Fatal("distinct paths reported equal")
	}
}

func TestTraversalBlockFindsPrivateAncestor(t *testing.T) {
	root := t.TempDir()
	priv := filepath.Join(root, "private")
	if err := os.Mkdir(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(priv, "app")
	if err := os.Mkdir(leaf, 0o777); err != nil {
		t.Fatal(err)
	}
	// Owned by this process, so nothing blocks it.
	if _, blocked := traversalBlock(leaf, os.Getuid()); blocked {
		t.Fatal("a path the runtime user owns was reported as blocked")
	}
	// A different uid cannot enter a directory only its owner may enter. The
	// answer is the OUTERMOST such ancestor, which on macOS is the private
	// per-user temp directory above t.TempDir() rather than priv itself -- so
	// the assertion is on the contract, not on a platform's temp layout.
	blocker, blocked := traversalBlock(leaf, os.Getuid()+1)
	if !blocked {
		t.Fatal("a 0700 ancestor owned by someone else did not block")
	}
	if !strings.HasPrefix(leaf, blocker) {
		t.Fatalf("traversalBlock = %q, which is not an ancestor of %q", blocker, leaf)
	}
	fi, err := os.Stat(blocker)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o055 != 0 {
		t.Fatalf("%s is mode %04o, which anyone can enter", blocker, fi.Mode().Perm())
	}
	if _, blocked := traversalBlock(leaf, -1); blocked {
		t.Fatal("an unknown runtime user should block nothing")
	}
}

// ── registry transport ────────────────────────────────────────────────────────

func TestRegistryRetriesUntilItAnswers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			// What the bad path does: accept the connection, answer nothing.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"packages":[{"org":"frappe","appName":"wiki","latest_version":"3.1.0"}]}`))
	}))
	defer srv.Close()

	c := &FPMClient{RegistryURL: srv.URL, httpClient: newRegistryHTTPClient()}
	cat := c.FetchCatalog(context.Background())
	if cat.Offline {
		t.Fatalf("gave up instead of retrying: %v", cat.Err)
	}
	if len(cat.Packages) != 1 || cat.Packages[0].Version != "3.1.0" {
		t.Fatalf("catalog = %+v", cat.Packages)
	}
	if hits.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", hits.Load())
	}
}

func TestRegistryDoesNotRetryAnAnswer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &FPMClient{RegistryURL: srv.URL, httpClient: newRegistryHTTPClient()}
	cat := c.FetchCatalog(context.Background())
	if !cat.Offline {
		t.Fatal("a 404 should leave the catalog offline")
	}
	if hits.Load() != 1 {
		t.Fatalf("a refusal was retried %d times", hits.Load())
	}
}

func TestInterleaveFamiliesPutsIPv6First(t *testing.T) {
	ips := parseIPs(t, "1.1.1.1", "2.2.2.2", "2606::1", "2606::2", "3.3.3.3")
	got := interleaveFamilies(ips)
	want := []string{"2606::1", "1.1.1.1", "2606::2", "2.2.2.2", "3.3.3.3"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].IP.String() != w {
			t.Fatalf("order[%d] = %s, want %s", i, got[i].IP, w)
		}
	}
}

func parseIPs(t *testing.T, addrs ...string) []net.IPAddr {
	t.Helper()
	var out []net.IPAddr
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			t.Fatalf("bad test address %q", a)
		}
		out = append(out, net.IPAddr{IP: ip})
	}
	return out
}

func TestRegistryFailsOverToMirror(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer dead.Close()
	var mirrorHits atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		_, _ = w.Write([]byte(`{"packages":[{"org":"frappe","appName":"crm","latest_version":"1.2.3"}]}`))
	}))
	defer mirror.Close()

	t.Setenv("VYBENCH_FPM_REGISTRY", dead.URL+","+mirror.URL)
	c := NewFPMClient()
	if c.RegistryURL != dead.URL || len(c.Mirrors) != 1 || c.Mirrors[0] != mirror.URL {
		t.Fatalf("registries = %q %q", c.RegistryURL, c.Mirrors)
	}
	cat := c.FetchCatalog(context.Background())
	if cat.Offline {
		t.Fatalf("did not fail over to the mirror: %v", cat.Err)
	}
	if len(cat.Packages) != 1 || cat.Packages[0].Version != "1.2.3" {
		t.Fatalf("catalog = %+v", cat.Packages)
	}
	// Reached on the first round, not after the primary exhausted its retries.
	if mirrorHits.Load() != 1 {
		t.Fatalf("mirror hits = %d, want 1", mirrorHits.Load())
	}
}

func TestBenchAcceptsAppsRejectsReadOnlyTrees(t *testing.T) {
	bench := fakeBench(t, "frappe")
	if err := BenchAcceptsApps(bench); err != nil {
		t.Fatalf("a writable bench was rejected: %v", err)
	}
	if err := os.Chmod(filepath.Join(bench, "apps"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(bench, "apps"), 0o775) })
	err := BenchAcceptsApps(bench)
	if err == nil {
		t.Fatal("a read-only apps/ was accepted")
	}
	if !strings.Contains(err.Error(), "apps/") {
		t.Fatalf("error does not name the tree: %v", err)
	}
}

// stubTools puts fake tar/file executables first (and only) on PATH.
func stubTools(t *testing.T, scripts map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// seccompTar behaves like GNU tar under snapd's seccomp policy: archiving
// ./a works, archiving ./a/b (or deeper) dies with EPERM, because the parent
// directory is opened with the denied openat2. $3 is the member: tar -cf FILE MEMBER.
const seccompTar = `case "$3" in
  ./*/*) echo "tar: $3: Cannot stat: Operation not permitted" >&2; exit 2 ;;
esac
exit 0`

// The probe must reach the failing call. A probe of a single-component member
// passes on a broken install; this is the regression test for that gap.
func TestDoctorBackupToolsProbeIsDeepEnoughToFail(t *testing.T) {
	stubTools(t, map[string]string{"tar": seccompTar, "file": "echo \"$1: ASCII text\""})
	var r Report
	checkBackupTools(&r)
	if len(r.Findings) != 1 || r.Findings[0].Severity != SevError {
		t.Fatalf("a tar that fails on nested members was reported healthy: %+v", r.Findings)
	}
	if !strings.Contains(r.Findings[0].Detail, "Cannot stat: Operation not permitted") {
		t.Fatalf("the tar fault was not named: %s", r.Findings[0].Detail)
	}
}

func TestDoctorBackupToolsWork(t *testing.T) {
	stubTools(t, map[string]string{"tar": "exit 0", "file": "echo \"$1: ASCII text\""})
	var r Report
	checkBackupTools(&r)
	if len(r.Findings) != 1 || r.Findings[0].Severity != SevOK {
		t.Fatalf("working tools were not reported healthy: %+v", r.Findings)
	}
}

// Reproduces both failures seen on the snap: tar answering EPERM (openat2 denied
// by seccomp) and file(1) absent. Neither may be reported as anything but a
// named, non-repairable fault -- frappe's own message blames the database.
func TestDoctorBackupToolsFaultsAreNamed(t *testing.T) {
	stubTools(t, map[string]string{
		"tar": "echo 'tar: ./a: Cannot stat: Operation not permitted' >&2; exit 2",
	}) // no file at all
	var r Report
	checkBackupTools(&r)
	if len(r.Findings) != 1 || r.Findings[0].Severity != SevError {
		t.Fatalf("broken tools were not an error: %+v", r.Findings)
	}
	f := r.Findings[0]
	for _, want := range []string{"Cannot stat: Operation not permitted", "file cannot identify", "not found on PATH"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, f.Detail)
		}
	}
	if f.Fixable() {
		t.Error("a packaging fault must not offer an in-place repair")
	}
}

func TestDoctorBackupToolsFileWithoutMagicDatabase(t *testing.T) {
	// file(1) with no magic database prints to stderr and exits 1.
	stubTools(t, map[string]string{
		"tar":  "exit 0",
		"file": "echo 'file: could not find any valid magic files!' >&2; exit 1",
	})
	var r Report
	checkBackupTools(&r)
	if len(r.Findings) != 1 || r.Findings[0].Severity != SevError ||
		!strings.Contains(r.Findings[0].Detail, "could not find any valid magic files") {
		t.Fatalf("missing magic database not reported: %+v", r.Findings)
	}
}

// `vybench bench new` makes only the directories; the snap links apps/, env/
// and apps.txt on first use. doctor must not report that as a failure.
func TestDoctorFreshBenchIsNotAFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SNAP", t.TempDir()) // DetectPlatform() == snap
	bench := t.TempDir()
	for _, d := range []string{"sites", "config/pids", "logs"} {
		if err := os.MkdirAll(filepath.Join(bench, d), 0o775); err != nil {
			t.Fatal(err)
		}
	}
	var r Report
	checkLayout(bench, &r)
	if len(r.Findings) != 1 || r.Findings[0].Severity != SevOK || !strings.Contains(r.Findings[0].Title, "new") {
		t.Fatalf("a never-used bench was not recognised: %+v", r.Findings)
	}
}

// The leniency is for a bench with NOTHING set up. Anything present means it
// was set up and then damaged, which is a real fault.
func TestDoctorPartlyLinkedBenchIsStillAFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SNAP", t.TempDir())
	bench := t.TempDir()
	for _, d := range []string{"sites", "config/pids", "logs", "env"} { // env present, apps gone
		if err := os.MkdirAll(filepath.Join(bench, d), 0o775); err != nil {
			t.Fatal(err)
		}
	}
	var r Report
	checkLayout(bench, &r)
	if !hasTitle(r, "has no apps") {
		t.Fatalf("a damaged bench (env present, apps missing) was excused as new: %+v", r.Findings)
	}
}
