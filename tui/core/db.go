package core

// Database reachability, starting the database, and explaining failures.
// Most failed site operations fail because the database server is not
// running, not because of the site; checking first turns a traceback and a
// half-made site into a one-line message and a key to press.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DBLabel is the product name of a database engine.
func DBLabel(engine string) string {
	if engine == "postgres" {
		return "PostgreSQL"
	}
	return "MariaDB"
}

// DatabaseReachable reports whether the database server a bench uses accepts
// connections. benchPath may be "" for a bench that does not exist yet.
func DatabaseReachable(benchPath, engine string) bool {
	up, _ := DatabaseStatus(benchPath, engine)
	return up
}

// DatabaseStatus is DatabaseReachable plus, when the server is down, what is
// in the way. A bench whose config names a db_socket is judged on that socket
// alone, because frappe connects only there: another MariaDB answering on
// port 3306 is a different server, with other data and other credentials.
func DatabaseStatus(benchPath, engine string) (bool, string) {
	var cfg map[string]any
	if benchPath != "" {
		cfg, _ = readConfig(commonConfigPath(benchPath))
	}
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	host := str("db_host")
	if host == "" {
		host = "127.0.0.1"
	}
	port := configInt(cfg, "db_port")

	if engine == "postgres" {
		if port == 0 {
			port = 5432
		}
		for _, dir := range []string{os.Getenv("PGHOST"), "/tmp", "/var/run/postgresql"} {
			if strings.HasPrefix(dir, "/") && dialOK("unix", filepath.Join(dir, fmt.Sprintf(".s.PGSQL.%d", port))) {
				return true, ""
			}
		}
		return dialOK("tcp", net.JoinHostPort(host, strconv.Itoa(port))), ""
	}

	if port == 0 {
		port = 3306
		if DetectPlatform() == PlatformBrew {
			port = BrewDBPort()
		}
	}
	tcp := net.JoinHostPort(host, strconv.Itoa(port))
	if sock := str("db_socket"); sock != "" {
		if dialOK("unix", sock) {
			return true, ""
		}
		if dialOK("tcp", tcp) {
			return false, fmt.Sprintf("another program holds port %d", port)
		}
		return false, ""
	}
	var sockets []string
	sockets = append(sockets, os.Getenv("MYSQL_UNIX_PORT"))
	switch DetectPlatform() {
	case PlatformBrew:
		sockets = append(sockets, brewRunSocket(), "/tmp/mysql.sock", filepath.Join(BrewPrefix(), "var", "mysql", "mysql.sock"))
	case PlatformSnap:
		sockets = append(sockets, filepath.Join(os.Getenv("SNAP_COMMON"), "run", "mysql.sock"))
	default:
		sockets = append(sockets, "/run/mysqld/mysqld.sock", "/var/run/mysqld/mysqld.sock", "/var/lib/mysql/mysql.sock")
	}
	for _, s := range sockets {
		if s != "" && dialOK("unix", s) {
			return true, ""
		}
	}
	return dialOK("tcp", tcp), ""
}

// brewRunSocket is the socket vybench's own MariaDB listens on (Homebrew).
func brewRunSocket() string {
	run := os.Getenv("VYBENCH_RUN")
	if run == "" {
		run = filepath.Join(BrewPrefix(), "var", "run", InstanceName())
	}
	return filepath.Join(run, "mysql.sock")
}

// BrewDBPort and BrewRedisPort are the ports of vybench's own MariaDB and
// Redis on Homebrew: ports of their own, so Homebrew's mariadb and redis
// services (3306, 6379) can run alongside.
func BrewDBPort() int { return envPort("VYBENCH_DB_PORT", 13306) }

// BrewRedisPort is the port of vybench's own Redis on Homebrew.
func BrewRedisPort() int { return envPort("VYBENCH_REDIS_PORT", 16379) }

func envPort(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

// brewVarDir is $VYBENCH_VAR on Homebrew.
func brewVarDir() string {
	if v := os.Getenv("VYBENCH_VAR"); v != "" {
		return v
	}
	return filepath.Join(BrewPrefix(), "var", InstanceName())
}

// OwnMariaDB reports whether vybench runs its own MariaDB on Homebrew, i.e.
// its data directory has been initialised. Sites then live there, and a
// different server on port 3306 must not be used in its place.
func OwnMariaDB() bool {
	return isDir(filepath.Join(brewVarDir(), "mariadb", "mysql"))
}

func dialOK(network, addr string) bool {
	conn, err := net.DialTimeout(network, addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func configInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// WaitForDatabaseStep waits until the bench's database accepts connections.
func WaitForDatabaseStep(benchPath, engine string, timeout time.Duration) Step {
	return Step{
		Label: "Waiting for " + DBLabel(engine) + " to accept connections",
		Fn: func(log func(string)) error {
			deadline := time.Now().Add(timeout)
			for next := time.Now(); time.Now().Before(deadline); time.Sleep(time.Second) {
				if DatabaseReachable(benchPath, engine) {
					log(DBLabel(engine) + " is up")
					return nil
				}
				if time.Now().After(next) {
					log("still waiting…")
					next = time.Now().Add(10 * time.Second)
				}
			}
			return fmt.Errorf("%s did not start within %s", DBLabel(engine), timeout)
		},
	}
}

// StartDatabaseSteps starts the services a bench's database needs and waits
// for it. For MariaDB that is the whole stack, the same as `vybench start`;
// on Homebrew the vybench service runs its own MariaDB when none is running.
func (s *Supervisor) StartDatabaseSteps(benchPath, engine string) ([]Step, error) {
	var steps []Step
	if engine == "postgres" {
		switch s.platform {
		case PlatformBrew:
			if _, err := exec.LookPath("brew"); err != nil {
				return nil, err
			}
			steps = []Step{{Cmd: exec.Command("brew", "services", "start", "postgresql@16")}}
		case PlatformSnap:
			steps = []Step{{Cmd: exec.Command("snapctl", "start", s.name+".postgres")}}
		case PlatformNative:
			steps = []Step{{Cmd: exec.Command("systemctl", "start", "postgresql")}}
		default:
			return nil, ErrUnsupportedPlatform
		}
	} else {
		var err error
		if steps, err = s.ControlSteps("start"); err != nil {
			return nil, err
		}
	}
	return append(steps, WaitForDatabaseStep(benchPath, engine, 90*time.Second)), nil
}

// clearStaleSocket removes a socket file left behind by a MariaDB that
// stopped: the supervisor would read it as "already running".
func (s *Supervisor) clearStaleSocket(log func(string)) error {
	if sock := brewRunSocket(); fileExists(sock) && !dialOK("unix", sock) {
		if err := os.Remove(sock); err != nil {
			return err
		}
		log("Removed a socket file left behind by a stopped server: " + sock)
	}
	return nil
}

// Diagnose explains a failed command from the end of its output, or returns
// "" when nothing known matches.
func Diagnose(lines []string) string {
	start := max(len(lines)-120, 0)
	text := strings.ToLower(strings.Join(lines[start:], "\n"))
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(text, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("(2002", "(2003", "can't connect to local server", "can't connect to mysql server",
		"can't connect to server", "could not connect to server", "connection refused"):
		return "The database server is not running; start it with [s] on Overview and try again"
	case has("access denied for user '_"):
		// frappe names each site's database user after its database ("_" +
		// hash): the site's own login is unknown to the server, i.e. its
		// database was never created there.
		return "The site's own database login does not exist: its install never finished. Drop it with [d] or create it again with [n]"
	case has("(1045", "access denied for user"):
		// Most often the DB root password typed into the dialog. Leaving that
		// field blank makes vybench use its own managed one, which is the usual fix.
		return "The database refused the login. If you typed a DB root password in the dialog it is wrong: leave that field blank to use vybench's own. Otherwise check root_password and db_socket in common_site_config.json"
	case has("already exists, use `--force`"):
		return "The site already exists; turn on Force to replace it"
	case has("do you want to continue anyway", "downgrade"):
		return "The backup is from an older Frappe version; turn on Force to restore it anyway"
	case has("no module named"):
		return "A Python module is missing from the bench's environment"
	case has("no bench command found"):
		return "vybench's bench command is not installed"
	}
	return ""
}
