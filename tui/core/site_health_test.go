package core

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestClassifySiteCheck(t *testing.T) {
	fail := errors.New("exit status 1")
	for _, c := range []struct {
		name      string
		out       string
		err       error
		want      SiteState
		dbMissing bool
	}{
		{"working", "Loading…\n{\"s.localhost\": [\"frappe\", \"erpnext\"]}\n", nil, SiteHealthy, false},
		{"frappe missing", `{"s.localhost": ["erpnext"]}`, nil, SiteIncomplete, false},
		{"no database", "Traceback…\npymysql.err.OperationalError: (1049, \"Unknown database '_abc'\")", fail, SiteIncomplete, true},
		{"half installed", "MySQLdb.ProgrammingError: (1146, \"Table '_abc.tabSingles' doesn't exist\")", fail, SiteIncomplete, false},
		{"pg no database", `psycopg2.OperationalError: FATAL:  database "_abc" does not exist`, fail, SiteIncomplete, true},
		{"pg half installed", `psycopg2.errors.UndefinedTable: relation "tabSingles" does not exist`, fail, SiteIncomplete, false},
		// Not installation failures: never treat these as incomplete.
		{"server down", "MySQLdb.OperationalError: (2002, \"Can't connect to local server through socket '/x/mysql.sock' (61)\")", fail, SiteUnknown, false},
		{"bad password", "MySQLdb.OperationalError: (1045, \"Access denied for user '_abc'@'localhost'\")", fail, SiteUnknown, false},
		{"garbage", "nothing useful", nil, SiteUnknown, false},
	} {
		got := classifySiteCheck("s.localhost", []byte(c.out), c.err)
		if got.State != c.want || got.DBMissing != c.dbMissing {
			t.Errorf("%s: got %+v, want state %v dbMissing %v", c.name, got, c.want, c.dbMissing)
		}
	}
	if c := classifySiteCheck("s", []byte("x\nMySQLdb.OperationalError: (2002, \"boom\")\n\n"), fail); !strings.Contains(c.Reason, "2002") {
		t.Errorf("reason should be the last line: %q", c.Reason)
	}
}

func TestQuickSiteCheck(t *testing.T) {
	bench := t.TempDir()
	if c, ok := QuickSiteCheck(bench, "none.localhost"); !ok || c.State != SiteIncomplete || !c.DBMissing {
		t.Errorf("no site_config.json: %+v %v", c, ok)
	}
	writeFile(t, filepath.Join(bench, "sites", "a", "site_config.json"), `{}`)
	if c, ok := QuickSiteCheck(bench, "a"); !ok || c.State != SiteIncomplete {
		t.Errorf("no db_name: %+v %v", c, ok)
	}
	writeFile(t, filepath.Join(bench, "sites", "b", "site_config.json"), `{"db_name": "_b"}`)
	if _, ok := QuickSiteCheck(bench, "b"); ok {
		t.Error("a site with a db_name needs the database to decide")
	}
	writeFile(t, filepath.Join(bench, "sites", "c", "site_config.json"), `{"db_name": "_c", "installed_apps": ["frappe", "erpnext"]}`)
	if c, ok := QuickSiteCheck(bench, "c"); !ok || c.State != SiteHealthy || c.Reason != "frappe is installed" || !slices.Equal(c.Apps, []string{"frappe", "erpnext"}) {
		t.Errorf("installed_apps not parsed: %+v %v", c, ok)
	}
	writeFile(t, filepath.Join(bench, "sites", "d", "site_config.json"), `{"db_name": "_d", "installed_apps": []}`)
	if _, ok := QuickSiteCheck(bench, "d"); ok {
		t.Error("empty installed_apps needs the database to decide")
	}
	writeFile(t, filepath.Join(bench, "sites", "e", "site_config.json"), `{"db_name": "_e", "installed_apps": [123]}`)
	if _, ok := QuickSiteCheck(bench, "e"); ok {
		t.Error("invalid installed_apps types needs the database to decide")
	}
	writeFile(t, filepath.Join(bench, "sites", "bad", "site_config.json"), `{bad json`)
	if c, ok := QuickSiteCheck(bench, "bad"); !ok || c.State != SiteUnknown {
		t.Errorf("bad json: %+v %v", c, ok)
	}
}

// CheckSite end to end, through a stand-in for the vybench CLI.
func TestCheckSiteRunsListApps(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "vybench")
	writeFile(t, cli, `#!/bin/sh
echo "$@" > "$(dirname "$0")/args"
case "$FAKE_MODE" in
  ok) echo '{"b.localhost": ["frappe"]}' ;;
  nodb) echo "pymysql.err.OperationalError: (1049, \"Unknown database '_b'\")" >&2; exit 1 ;;
  down) echo "MySQLdb.OperationalError: (2002, \"Can't connect\")" >&2; exit 1 ;;
esac
`)
	_ = os.Chmod(cli, 0o755)
	t.Setenv("VYBENCH_CLI", cli)
	t.Setenv("SNAP", "")
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "b.localhost", "site_config.json"), `{"db_name": "_b"}`)

	for mode, want := range map[string]SiteState{"ok": SiteHealthy, "nodb": SiteIncomplete, "down": SiteUnknown} {
		t.Setenv("FAKE_MODE", mode)
		if got := CheckSite(bench, "b.localhost"); got.State != want {
			t.Errorf("%s: %+v", mode, got)
		}
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(cli), "args"))
	if strings.TrimSpace(string(args)) != "bench --site b.localhost list-apps --format json" {
		t.Errorf("args = %q", args)
	}
}

func TestNewSiteSpecReplacesInPlace(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "x.localhost", "site_config.json"), `{"db_name": "_x1"}`)
	opts := NewSiteOptions{Name: "x.localhost", AdminPassword: "pw"}
	if _, err := NewSiteSpec(bench, opts); !errors.Is(err, ErrSiteExists) {
		t.Fatalf("err = %v", err)
	}
	opts.Force, opts.BackupFirst = true, true
	spec, err := NewSiteSpec(bench, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps) != 2 || !slices.Contains(spec.Steps[0].Cmd.Args, "backup") {
		t.Fatalf("steps %+v", spec.Steps)
	}
	args := spec.Steps[1].Cmd.Args
	if i := slices.Index(args, "--db-name"); i < 0 || args[i+1] != "_x1" || !slices.Contains(args, "--force") {
		t.Errorf("replace args = %v", args)
	}
}

func TestDropSiteSpec(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	spec, _ := DropSiteSpec(bench, "s", false, SiteCheck{State: SiteHealthy})
	if a := spec.Steps[0].Cmd.Args; !slices.Contains(a, "drop-site") || slices.Contains(a, "--force") {
		t.Errorf("args %v", a)
	}
	spec, _ = DropSiteSpec(bench, "s", true, SiteCheck{})
	if !slices.Contains(spec.Steps[0].Cmd.Args, "--force") {
		t.Error("force not passed")
	}
	spec, _ = DropSiteSpec(bench, "s", true, SiteCheck{State: SiteIncomplete, DBMissing: true})
	if spec.Steps[0].Cmd != nil || spec.Steps[0].Fn == nil {
		t.Error("a site without a database must be archived, not dropped by bench")
	}
}

func TestListBackupsAndFilesNextTo(t *testing.T) {
	bench := t.TempDir()
	dir := BackupDir(bench, "s.localhost")
	for _, n := range []string{
		"20260910_080000-s_localhost-database.sql.gz",
		"20260911_090000-s_localhost-database-enc.sql.gz",
		"20260911_090000-s_localhost-files-enc.tgz",
		"20260911_090000-s_localhost-private-files-enc.tgz",
		"20260911_090000-s_localhost-site_config_backup-enc.json",
		"20260912_100000-s_localhost-partial-database.sql.gz", // not restorable
		"20260913_100000-s_localhost-files.tar",               // files without a database
		"notes.txt",
	} {
		writeFile(t, filepath.Join(dir, n), "data")
	}
	b := ListBackups(bench, "s.localhost")
	if len(b) != 2 || b[0].Stamp != "20260911_090000" || !b[0].Encrypted || !b[0].HasFiles() || b[1].HasFiles() {
		t.Fatalf("backups = %+v", b)
	}
	if !strings.Contains(b[0].Label(), "2026-09-11 09:00") || !strings.Contains(b[0].Label(), "with files") {
		t.Errorf("label = %q", b[0].Label())
	}
	pub, priv := FilesNextTo(b[0].Database)
	if filepath.Base(pub) != "20260911_090000-s_localhost-files-enc.tgz" || !strings.HasSuffix(priv, "private-files-enc.tgz") {
		t.Errorf("FilesNextTo = %q %q", pub, priv)
	}
	if p, q := FilesNextTo(b[1].Database); p != "" || q != "" {
		t.Error("files found for a database-only backup")
	}
}

func TestRestoreSpec(t *testing.T) {
	fakeCLI(t)
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "s.localhost", "site_config.json"), `{"db_name": "_s"}`)
	sql := filepath.Join(t.TempDir(), "20260101_000000-x-database.sql.gz")
	writeFile(t, sql, "x")

	if _, err := RestoreSpec(bench, RestoreOptions{Site: "s.localhost"}); err == nil {
		t.Error("no backup chosen, no error")
	}
	if _, err := RestoreSpec(bench, RestoreOptions{Site: "s.localhost", SQLPath: "/nope.sql.gz"}); err == nil {
		t.Error("missing file, no error")
	}
	spec, err := RestoreSpec(bench, RestoreOptions{Site: "s.localhost", SQLPath: sql, BackupFirst: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps) != 2 || !slices.Contains(spec.Steps[0].Cmd.Args, "backup") {
		t.Fatalf("steps %+v", spec.Steps)
	}
	args := spec.Steps[1].Cmd.Args
	i := slices.Index(args, "--site")
	if i < 0 || !slices.Equal(args[i:], []string{"--site", "s.localhost", "restore", sql, "--force"}) {
		t.Errorf("args %v", args)
	}
	// A new site has nothing to back up.
	spec, _ = RestoreSpec(bench, RestoreOptions{Site: "new.localhost", SQLPath: sql, BackupFirst: true})
	if len(spec.Steps) != 1 {
		t.Error("backed up a site that does not exist")
	}
}

func TestListSiteDirsIncludesLeftovers(t *testing.T) {
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "ok.localhost", "site_config.json"), "{}")
	if err := os.MkdirAll(filepath.Join(bench, "sites", "half.localhost", "locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bench, "sites", "random-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ListSiteDirs(bench); !slices.Equal(got, []string{"half.localhost", "ok.localhost"}) {
		t.Errorf("ListSiteDirs = %v", got)
	}
	if got := DiscoverSites(bench); !slices.Equal(got, []string{"ok.localhost"}) {
		t.Errorf("DiscoverSites = %v", got)
	}
}

func TestDiagnose(t *testing.T) {
	for out, want := range map[string]string{
		"MySQLdb.OperationalError: (2002, \"Can't connect to local server through socket\")": "not running",
		"(1045, \"Access denied for user 'root'@'localhost'\")":                              "refused the login",
		"(1045, \"Access denied for user '_b4c3c8081b08088f'@'localhost'\")":                 "install never finished",
		"Site x already exists, use `--force` to proceed anyway":                             "turn on Force",
		"Do you want to continue anyway? [y/N]: Aborted!":                                    "older Frappe",
		"everything fine": "",
	} {
		got := Diagnose(strings.Split(out, "\n"))
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("Diagnose(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestDatabaseReachable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mysql.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer ln.Close()
	bench := t.TempDir()
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"db_socket": "`+sock+`"}`)
	if !DatabaseReachable(bench, "mariadb") {
		t.Error("a listening socket was not seen")
	}

	// PostgreSQL on a port nothing listens on.
	tcp, _ := net.Listen("tcp", "127.0.0.1:0")
	port := tcp.Addr().(*net.TCPAddr).Port
	tcp.Close()
	t.Setenv("PGHOST", t.TempDir())
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"db_port": `+strconv.Itoa(port)+`}`)
	if DatabaseReachable(bench, "postgres") {
		t.Error("a closed port was reported reachable")
	}
}

// Regression: with Homebrew's separate MariaDB on the port, the check said
// "running" while every site command failed on the bench's dead socket.
func TestDatabaseStatusJudgesTheBenchSocket(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer tcp.Close()
	port := tcp.Addr().(*net.TCPAddr).Port
	bench := t.TempDir()
	dead := filepath.Join(t.TempDir(), "mysql.sock")
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"),
		`{"db_socket": "`+dead+`", "db_host": "127.0.0.1", "db_port": `+strconv.Itoa(port)+`}`)
	up, why := DatabaseStatus(bench, "mariadb")
	if up || why != "another program holds port "+strconv.Itoa(port) {
		t.Errorf("up=%v why=%q", up, why)
	}
}

// On vybench's own MariaDB a missing database folder means the site's install
// never finished, even while the server is down; the error the site gives
// then is "access denied" for its own user, not "unknown database".
func TestQuickSiteCheckLooksForTheDatabaseFolder(t *testing.T) {
	if DetectPlatform() != PlatformBrew {
		t.Skip("uses the Homebrew data directory layout")
	}
	varDir, run := t.TempDir(), t.TempDir()
	t.Setenv("VYBENCH_VAR", varDir)
	t.Setenv("VYBENCH_RUN", run)
	if err := os.MkdirAll(filepath.Join(varDir, "mariadb", "mysql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(varDir, "mariadb", "_live"), 0o755); err != nil {
		t.Fatal(err)
	}
	bench := t.TempDir()
	sock := filepath.Join(run, "mysql.sock")
	writeFile(t, filepath.Join(bench, "sites", "common_site_config.json"), `{"db_socket": "`+sock+`"}`)
	writeFile(t, filepath.Join(bench, "sites", "half", "site_config.json"), `{"db_name": "_gone"}`)
	writeFile(t, filepath.Join(bench, "sites", "ok", "site_config.json"), `{"db_name": "_live"}`)
	writeFile(t, filepath.Join(bench, "sites", "elsewhere", "site_config.json"), `{"db_name": "_x", "db_socket": "/tmp/mysql.sock"}`)

	if c, ok := QuickSiteCheck(bench, "half"); !ok || c.State != SiteIncomplete || !c.DBMissing {
		t.Errorf("missing database folder: %+v %v", c, ok)
	}
	if _, ok := QuickSiteCheck(bench, "ok"); ok {
		t.Error("a site with its database folder was judged from files")
	}
	if _, ok := QuickSiteCheck(bench, "elsewhere"); ok {
		t.Error("a site on another server was judged by vybench's data directory")
	}
	if c := classifySiteCheck("s", []byte("(1045, \"Access denied for user '_gone'@'localhost'\")"), errors.New("x")); c.State != SiteUnknown {
		t.Errorf("list-apps alone must not call 1045 an unfinished install: %+v", c)
	}
}
