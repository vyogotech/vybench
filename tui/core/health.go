package core

// Bench health for the Overview: what `brew services` cannot tell. A service
// can be "started" while the database its bench uses is down, or while it
// serves a different bench than the active one.

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// BenchHealth is the state of the things a bench needs, seen from the bench.
type BenchHealth struct {
	DBUp    bool
	DBWhy   string // what is in the way when the database is down
	WebPort int
	WebUp   bool   // something answers on the bench's webserver_port
	Serving string // path of the bench the supervisor's web process runs, "" if unknown
}

// CheckBench gathers a bench's health. candidates are the benches the
// supervisor might be serving; it shells out nothing, but dials sockets, so
// call it off the UI thread.
func CheckBench(benchPath, engine string, candidates []string) BenchHealth {
	h := BenchHealth{WebPort: WebserverPort(benchPath)}
	h.DBUp, h.DBWhy = DatabaseStatus(benchPath, engine)
	h.WebUp = dialOK("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.WebPort)))
	h.Serving = ServedBench(candidates)
	return h
}

// ServedBench returns the bench whose web process is alive, judged by the
// pid files the Homebrew supervisor writes to <bench>/config/pids.
func ServedBench(paths []string) string {
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(p, "config", "pids", "web.pid"))
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && processAlive(pid) {
			return p
		}
	}
	return ""
}
