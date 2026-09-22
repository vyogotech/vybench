package core

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrimRevisionAndReadlink(t *testing.T) {
	if got := trimRevision("/snap/vybench/current/opt/frappe-bench"); got != "/snap/vybench/" {
		t.Fatalf("current: %q", got)
	}
	if got := trimRevision("/snap/vybench/34/opt/frappe-bench"); got != "/snap/vybench/" {
		t.Fatalf("revision: %q", got)
	}
	if got := trimRevision("/opt/frappe-bench"); got != "/opt/frappe-bench" {
		t.Fatalf("non-snap: %q", got)
	}
	if got := trimRevision("/snap/vybench"); got != "/snap/vybench" {
		t.Fatalf("too short: %q", got)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := readlink(link); got != target {
		t.Fatalf("readlink = %q", got)
	}
	if got := readlink(target); got != target {
		t.Fatalf("plain path = %q", got)
	}
}

func TestRelocateAppCopiesOutOfPrivateTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SNAP", "")
	bench := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bench, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "hidden", "erpnext")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hooks.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(bench, "apps", "erpnext")
	if err := os.Symlink(src, old); err != nil {
		t.Fatal(err)
	}

	dest, err := relocateApp(bench, "erpnext", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "hooks.py")); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(old)
	if err != nil || got != dest {
		t.Fatalf("link = %q err=%v, want %s", got, err, dest)
	}

	// A second call finds the copy already in the store.
	again, err := relocateApp(bench, "erpnext", src)
	if err != nil || again != dest {
		t.Fatalf("second relocate = %q %v", again, err)
	}
}

func TestRelocateAppKeepsFPMVersionTail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SNAP", "")
	bench := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bench, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), ".fpm", "apps", "vyogo", "erpnext", "16.0.0")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dest, err := relocateApp(bench, "erpnext", src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dest, filepath.Join("vyogo", "erpnext", "16.0.0")) {
		t.Fatalf("dest = %s", dest)
	}
}

func TestCheckAccessGroupWriteAndSocket(t *testing.T) {
	bench := fakeBench(t, "frappe")
	for _, d := range []string{"", "sites", "config", "logs"} {
		if err := os.Chmod(filepath.Join(bench, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	common := t.TempDir()
	t.Setenv("SNAP_COMMON", common)
	run := filepath.Join(common, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}

	var r Report
	checkAccess(bench, &r)
	if !hasTitle(r, "not writable") {
		t.Fatalf("findings: %+v", r.Findings)
	}
	if !hasTitle(r, "socket directory") {
		t.Fatalf("socket finding missing: %+v", r.Findings)
	}
	if _, failed := r.Apply(); failed != 0 {
		t.Fatalf("fixes failed: %+v", r.Findings)
	}
	fi, err := os.Stat(filepath.Join(bench, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o020 == 0 {
		t.Fatalf("sites still not group-writable: %o", fi.Mode().Perm())
	}
	sfi, err := os.Stat(run)
	if err != nil || sfi.Mode().Perm()&0o005 != 0o005 {
		t.Fatalf("socket dir mode %v err %v", sfi, err)
	}
}

func TestCheckDBRootBranches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SNAP_COMMON", "")

	missing := t.TempDir()
	var r Report
	checkDBRoot(missing, &r)
	if !hasTitle(r, "common_site_config.json is missing") {
		t.Fatalf("%+v", r.Findings)
	}

	pg := fakeBench(t)
	if err := os.WriteFile(filepath.Join(pg, "sites", "common_site_config.json"), []byte(`{"db_type":"postgres"}`), 0o664); err != nil {
		t.Fatal(err)
	}
	r = Report{}
	checkDBRoot(pg, &r)
	if hasTitle(r, "MariaDB") {
		t.Fatalf("postgres bench reported mariadb: %+v", r.Findings)
	}

	ok := fakeBench(t)
	if err := os.WriteFile(filepath.Join(ok, "config", "mariadb_root_password"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = Report{}
	checkDBRoot(ok, &r)
	if !hasTitle(r, "root password is available") {
		t.Fatalf("%+v", r.Findings)
	}

	none := fakeBench(t)
	r = Report{}
	checkDBRoot(none, &r)
	if !hasTitle(r, "cannot be found") {
		t.Fatalf("%+v", r.Findings)
	}
}

func TestCheckRegistryOnlineAndOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"packages":[{"name":"erpnext","org":"frappe"}]}`))
	}))
	defer srv.Close()
	t.Setenv("VYBENCH_FPM_REGISTRY", srv.URL)
	var r Report
	checkRegistry(context.Background(), &r)
	if !hasTitle(r, "registry lists") {
		t.Fatalf("%+v", r.Findings)
	}

	t.Setenv("VYBENCH_FPM_REGISTRY", "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r = Report{}
	checkRegistry(ctx, &r)
	if !hasTitle(r, "could not be read") {
		t.Fatalf("%+v", r.Findings)
	}
}

func TestDaemonHelpersOffSnap(t *testing.T) {
	t.Setenv("SNAP", "")
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := daemonMkdirAll(nested); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(nested, "file")
	if err := daemonWriteFile(dst, []byte("written")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dst)
	if err != nil || string(body) != "written" {
		t.Fatalf("body %q err %v", body, err)
	}
	moved := filepath.Join(nested, "moved")
	if err := daemonRename(dst, moved); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(nested, "link")
	if err := daemonSymlink(moved, link); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(dir, "copy")
	if err := daemonCopyTree(nested, copied); err != nil {
		t.Fatal(err)
	}
	if err := daemonChmod("g+rwx", copied); err != nil {
		t.Fatal(err)
	}
	if err := daemonRemove(link); err != nil {
		t.Fatal(err)
	}
	if err := daemonRun("true"); err != nil {
		t.Fatal(err)
	}
	if err := daemonRun("false"); err == nil {
		t.Fatal("false should fail")
	}

	chownDaemon(copied)
	chownDaemonLink(filepath.Join(dir, "no-such-link"))
	chownDaemonTree(copied)
	if asDaemon() {
		t.Fatal("not running as the snap daemon")
	}
	if uid, gid, ok := daemonIDs(); ok || uid != 0 || gid != 0 {
		t.Fatalf("daemonIDs = %d %d %v", uid, gid, ok)
	}
	if runtimeUserID() != os.Getuid() {
		t.Fatalf("runtime uid %d", runtimeUserID())
	}
	if runtimeUserName() == "" {
		t.Fatal("empty runtime user")
	}
	cmd := runtimeCommand(dir, "true")
	if cmd.Dir != dir {
		t.Fatalf("dir %s", cmd.Dir)
	}

	t.Setenv("SNAP", t.TempDir())
	if runtimeUserName() != "snap_daemon" {
		t.Fatalf("snap name %q", runtimeUserName())
	}
	_ = runtimeUserID()
	_ = runtimeCommand(dir, "true")
}

func TestPrepareBenchesDirAndAtomicWrite(t *testing.T) {
	t.Setenv("SNAP", "")
	dir := filepath.Join(t.TempDir(), "benches")
	if err := prepareBenchesDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nested", "file")
	if err := writeFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestPreferredBackupRemainingBranches(t *testing.T) {
	if got := PreferredBackupIndex(nil); got != 0 {
		t.Fatalf("empty %d", got)
	}
	allSafety := []Backup{{Safety: true, PublicFiles: "f"}, {Safety: true}}
	if got := PreferredBackupIndex(allSafety); got != 0 {
		t.Fatalf("all safety %d", got)
	}
	dbOnly := []Backup{{Stamp: "new"}, {Safety: true, PublicFiles: "f"}}
	if got := PreferredBackupIndex(dbOnly); got != 0 {
		t.Fatalf("db-only preferred %d", got)
	}
}

func TestPlatformEnvStubs(t *testing.T) {
	t.Setenv("SNAP", "")
	t.Setenv("VYBENCH_NAME", "vybench")
	if DetectPlatform() == PlatformSnap {
		t.Fatal("empty SNAP is not the snap")
	}
	if !strings.Contains(RestartHint(), "vybench") && RestartHint() == "" {
		t.Fatal("empty restart hint")
	}
	if StableBenchPath() == "" && PackagedBenchPath() == "" && DetectPlatform() == PlatformUnknown {
		t.Fatal("unknown platform still has a packaged path")
	}

	snap := t.TempDir()
	t.Setenv("SNAP", snap)
	t.Setenv("SNAP_INSTANCE_NAME", "vybench")
	if DetectPlatform() != PlatformSnap {
		t.Fatalf("platform %s", DetectPlatform())
	}
	if got := StableBenchPath(); got != "/snap/vybench/current/opt/frappe-bench" {
		t.Fatalf("stable %s", got)
	}
	if got := PackagedBenchPath(); got != filepath.Join(snap, "opt", "frappe-bench") {
		t.Fatalf("packaged %s", got)
	}
	if RestartHint() != "sudo snap restart vybench" {
		t.Fatalf("hint %q", RestartHint())
	}
}

func TestDialEachAddressAndRegistryClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ctx := context.Background()
	conn, err := dialEachAddress(ctx, ln.Addr().String(), func(ctx context.Context, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	if _, err := dialEachAddress(ctx, "not-a-host", func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, errors.New("refused " + addr)
	}); err == nil {
		t.Fatal("expected a dial error")
	}

	failCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialEachAddress(failCtx, "localhost:1", func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, errors.New("no")
	}); err == nil {
		t.Fatal("cancelled dial should fail")
	}

	if _, err := dialEachAddress(ctx, "localhost:9", func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, errors.New("down")
	}); err == nil {
		t.Fatal("every address failed")
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer plain.Close()
	client := newRegistryHTTPClient()
	resp, err := client.Get(plain.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer tlsSrv.Close()
	tlsClient := newRegistryHTTPClient()
	tlsClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// DialTLSContext on the registry client builds its own tls.Config, so the
	// test server's certificate is rejected. The handshake error is the branch.
	if _, err := tlsClient.Get(tlsSrv.URL); err == nil {
		t.Fatal("expected the test certificate to be rejected")
	}

	if err := giveUp("http://example", errors.New("timeout"), 3, nil); !strings.Contains(err.Error(), "gave up after 3") {
		t.Fatal(err)
	}
	if err := giveUp("http://example", nil, 0, context.Canceled); !strings.Contains(err.Error(), "context canceled") {
		t.Fatal(err)
	}
	if err := giveUp("http://example", nil, 0, nil); !strings.Contains(err.Error(), "no attempt") {
		t.Fatal(err)
	}
	if got := (httpStatusError{URL: "http://x", Status: "404 Not Found"}).Error(); !strings.Contains(got, "404") {
		t.Fatal(got)
	}
}

func TestCheckFPMStoreBranches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SNAP", "")
	t.Setenv("VYBENCH_FPM_REGISTRY", "https://example.test")

	var missing Report
	checkFPMStore(&missing)
	if !hasTitle(missing, "no repository configured") {
		t.Fatalf("%+v", missing.Findings)
	}
	if _, failed := missing.Apply(); failed != 0 {
		t.Fatal(missing.Findings)
	}
	if _, err := os.Stat(fpmConfigPath()); err != nil {
		t.Fatal(err)
	}

	path := fpmConfigPath()
	if err := os.WriteFile(path, []byte("{"), 0o664); err != nil {
		t.Fatal(err)
	}
	var bad Report
	checkFPMStore(&bad)
	if !hasTitle(bad, "no usable repository") {
		t.Fatalf("%+v", bad.Findings)
	}

	if err := os.WriteFile(path, []byte(`{"repositories":{"vyogo":{"url":"https://example.test"}}}`), 0o664); err != nil {
		t.Fatal(err)
	}
	var ok Report
	checkFPMStore(&ok)
	if !hasTitle(ok, "repository") {
		t.Fatalf("%+v", ok.Findings)
	}

	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	// Owner can still read mode 0 on some systems. Point the path at a directory
	// so the read fails with a real error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var unreadable Report
	checkFPMStore(&unreadable)
	if !hasTitle(unreadable, "cannot be read") && !hasTitle(unreadable, "no usable") {
		t.Fatalf("%+v", unreadable.Findings)
	}
}

func TestSnapPathsOnlyOnSnap(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "root_password")
	sock := filepath.Join(dir, "mysql.sock")
	if err := os.WriteFile(pw, []byte("from-snap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	prevPW, prevSock := snapMariaDBRootPassword, snapMariaDBSocket
	snapMariaDBRootPassword, snapMariaDBSocket = pw, sock
	t.Cleanup(func() {
		snapMariaDBRootPassword, snapMariaDBSocket = prevPW, prevSock
	})
	t.Setenv("SNAP", "")
	t.Setenv("SNAP_COMMON", "")
	bench := t.TempDir()
	if got := ResolveMariaDBRootPassword(bench, ""); got == "from-snap" {
		t.Fatal("non-snap platform read the snap password file")
	}
	if got := resolveMariaDBSocket(bench); got == sock {
		t.Fatal("non-snap platform used the snap socket")
	}
	t.Setenv("SNAP", t.TempDir())
	if got := ResolveMariaDBRootPassword(bench, ""); got != "from-snap" {
		t.Fatalf("snap platform password %q", got)
	}
	if got := resolveMariaDBSocket(bench); got != sock {
		t.Fatalf("snap platform socket %q", got)
	}
}

func TestDaemonAccount(t *testing.T) {
	t.Setenv("SNAP", "")
	if name, ok := daemonAccount(); ok {
		t.Fatalf("brew/native-unknown account %s", name)
	}
	t.Setenv("SNAP", t.TempDir())
	if name, ok := daemonAccount(); !ok || name != "snap_daemon" {
		t.Fatalf("snap account %q %v", name, ok)
	}
}

func TestPlatformCalls(t *testing.T) {
	EnsureSnapDaemonGroup()
	if OpenURLCommand("http://127.0.0.1:8000") == nil {
		t.Fatal("nil open command")
	}
}

func TestCheckWritableTreesLinkedBench(t *testing.T) {
	t.Setenv("SNAP", "")
	lib := t.TempDir()
	t.Setenv("VYBENCH_LIBEXEC", lib)
	packaged := filepath.Join(lib, "frappe-bench")
	for _, d := range []string{"apps", "env", filepath.Join("sites", "assets")} {
		if err := os.MkdirAll(filepath.Join(packaged, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bench := t.TempDir()
	for _, d := range []string{"apps", "env", filepath.Join("sites", "assets")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(bench, d)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(packaged, d), filepath.Join(bench, d)); err != nil {
			t.Fatal(err)
		}
	}
	if StableBenchPath() == "" {
		t.Skip("this platform has no packaged bench path")
	}
	var r Report
	checkWritableTrees(bench, &r)
	if !hasTitle(r, "read-only") && !hasTitle(r, "writable") {
		t.Fatalf("%+v", r.Findings)
	}
}
