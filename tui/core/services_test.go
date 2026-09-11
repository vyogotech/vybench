package core

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestParseBrewServices(t *testing.T) {
	out := []byte(`[
	  {"name": "mariadb", "status": "started", "user": "me"},
	  {"name": "vybench-local", "status": "error", "user": "me"},
	  {"name": "postgresql@16", "status": "none"},
	  {"name": "unrelated", "status": "started"}
	]`)
	svcs, err := parseBrewServices(out, "vybench-local")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Service{}
	for _, s := range svcs {
		got[s.Name] = s
	}
	if got["vybench-local"].State != StateFailed || got["vybench-local"].Detail != "error" {
		t.Errorf("instance: %+v", got["vybench-local"])
	}
	if got["mariadb"].State != StateRunning {
		t.Errorf("mariadb: %+v", got["mariadb"])
	}
	if r := got["redis"]; r.State != StateStopped || r.Detail != "not installed" {
		t.Errorf("missing redis: %+v", r)
	}
	if got["postgresql@16"].State != StateStopped {
		t.Errorf("postgres: %+v", got["postgresql@16"])
	}
	if _, ok := got["unrelated"]; ok {
		t.Error("unrelated service listed")
	}
	if _, err := parseBrewServices([]byte("<html>"), "vybench"); err == nil {
		t.Error("garbage parsed")
	}
}

// "inactive" contains "active"; the old substring check reported stopped
// snap services as running.
func TestParseSnapServices(t *testing.T) {
	out := []byte(`Service               Startup   Current   Notes
vybench.mariadb       enabled   active    -
vybench.web           enabled   inactive  -
vybench.watch         disabled  inactive  -
vybench.worker-short  enabled   failed    -
`)
	svcs := parseSnapServices(out)
	if len(svcs) != 4 {
		t.Fatalf("got %d services", len(svcs))
	}
	want := []ServiceState{StateRunning, StateStopped, StateStopped, StateFailed}
	for i, s := range svcs {
		if s.State != want[i] {
			t.Errorf("%s: state %v, want %v", s.Name, s.State, want[i])
		}
	}
	if svcs[1].Label != "Web (gunicorn)" || svcs[2].Detail != "disabled" {
		t.Errorf("labels/details: %+v", svcs)
	}
}

func TestParseSystemdShow(t *testing.T) {
	out := []byte(`Id=frappe-web.service
LoadState=loaded
ActiveState=active
UnitFileState=enabled

Id=redis-server.service
LoadState=not-found
ActiveState=inactive
UnitFileState=

Id=frappe-scheduler.service
LoadState=loaded
ActiveState=failed
UnitFileState=disabled
`)
	svcs := parseSystemdShow(out)
	if len(svcs) != 2 {
		t.Fatalf("got %+v", svcs)
	}
	if svcs[0].State != StateRunning || svcs[0].Label != "Web (gunicorn)" {
		t.Errorf("web: %+v", svcs[0])
	}
	if svcs[1].State != StateFailed || svcs[1].Detail != "disabled" {
		t.Errorf("scheduler: %+v", svcs[1])
	}
}

func TestControlSteps(t *testing.T) {
	snap := &Supervisor{platform: PlatformSnap, name: "vybench", output: func(string, ...string) ([]byte, error) {
		return []byte(`Service          Startup   Current   Notes
vybench.mariadb  enabled   active    -
vybench.redis    enabled   active    -
vybench.web      enabled   active    -
vybench.watch    disabled  inactive  -
`), nil
	}}
	stop, err := snap.ControlSteps("stop")
	if err != nil {
		t.Fatal(err)
	}
	if args := stop[0].Cmd.Args; !slices.Equal(args[1:], []string{"stop", "vybench.web"}) {
		t.Errorf("snap stop = %v (datastores and disabled services must be left alone)", args)
	}
	start, _ := snap.ControlSteps("start")
	if args := strings.Join(start[0].Cmd.Args, " "); !strings.Contains(args, "vybench.mariadb") || strings.Contains(args, "watch") {
		t.Errorf("snap start = %s", args)
	}

	brew := &Supervisor{platform: PlatformBrew, name: "vybench"}
	steps, _ := brew.ControlSteps("start")
	if len(steps) != 2 || steps[0].Fn == nil || steps[1].Fn == nil {
		t.Fatalf("brew start steps: %+v", steps)
	}
	for _, st := range steps {
		if st.Cmd != nil {
			t.Errorf("start must only touch the vybench service, got %v", st.Cmd.Args)
		}
	}
	db, _ := brew.StartDatabaseSteps("", "mariadb")
	if len(db) != 3 || !strings.Contains(db[2].Label, "Waiting") {
		t.Errorf("starting the database must end by waiting for it: %+v", db)
	}
	if _, err := brew.ControlSteps("explode"); err == nil {
		t.Error("unknown action accepted")
	}
	if _, err := (&Supervisor{platform: PlatformUnknown}).Status(); err != ErrUnsupportedPlatform {
		t.Errorf("unknown platform: %v", err)
	}
}

func TestClearStaleSocket(t *testing.T) {
	t.Setenv("VYBENCH_RUN", t.TempDir())
	stale := filepath.Join(os.Getenv("VYBENCH_RUN"), "mysql.sock")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sup := &Supervisor{platform: PlatformBrew, name: "vybench"}
	if err := sup.clearStaleSocket(func(string) {}); err != nil {
		t.Fatal(err)
	}
	if fileExists(stale) {
		t.Error("the stale socket file was not removed")
	}
}

// The Overview used to repeat `brew services`, whose mariadb and redis are
// Homebrew's own servers, not the ones vybench runs on their own ports.
func TestBrewStatusReportsVybenchsOwnServers(t *testing.T) {
	t.Setenv("VYBENCH_RUN", t.TempDir())
	t.Setenv("VYBENCH_DB_PORT", "13306")
	t.Setenv("VYBENCH_REDIS_PORT", freePort(t))
	sup := &Supervisor{platform: PlatformBrew, name: "vybench-local", output: func(string, ...string) ([]byte, error) {
		return []byte(`[{"name":"mariadb","status":"started"},{"name":"redis","status":"started"},{"name":"vybench-local","status":"started"}]`), nil
	}}
	svcs, err := sup.Status()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range svcs {
		names = append(names, s.Name)
		if s.Name == "mariadb" || s.Name == "redis" {
			t.Errorf("Homebrew's own %s is reported", s.Name)
		}
		if (s.Name == "vybench-mariadb" || s.Name == "vybench-redis") && s.State != StateStopped {
			t.Errorf("%s reported %v with nothing listening", s.Name, s.State)
		}
	}
	if !slices.Equal(names, []string{"vybench-local", "vybench-mariadb", "vybench-redis"}) {
		t.Errorf("rows = %v", names)
	}
	if !strings.Contains(svcs[1].Label, "13306") {
		t.Errorf("MariaDB row does not show its port: %q", svcs[1].Label)
	}
}

// freePort returns a TCP port nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func TestServedBench(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(a, "config", "pids", "web.pid"), "999999")
	writeFile(t, filepath.Join(b, "config", "pids", "web.pid"), strconv.Itoa(os.Getpid()))
	if got := ServedBench([]string{a, b}); got != b {
		t.Errorf("ServedBench = %q, want the bench whose web process is alive", got)
	}
	if got := ServedBench([]string{a}); got != "" {
		t.Errorf("a dead pid counted as serving: %q", got)
	}
}
