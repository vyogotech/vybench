package core

// Service supervisor: reports and controls the vybench services on Homebrew,
// the snap, and the systemd units of the .deb/.rpm packages.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// ServiceState is the current state of a daemon.
type ServiceState int

const (
	StateUnknown ServiceState = iota
	StateRunning
	StateStopped
	StateFailed
	StateConflict // running, and in the way of something vybench needs
)

// Service is one daemon as reported by the platform's service manager.
type Service struct {
	Name   string // service manager's name, e.g. "vybench.web"
	Label  string // human-readable label
	State  ServiceState
	Detail string // e.g. "disabled"
}

// Supervisor reports and controls vybench services.
type Supervisor struct {
	platform Platform
	name     string
	// output runs a command and returns its stdout; replaced in tests.
	output func(name string, args ...string) ([]byte, error)
}

// NewSupervisor detects the platform and instance name from the environment.
func NewSupervisor() *Supervisor {
	return &Supervisor{platform: DetectPlatform(), name: InstanceName(), output: commandOutput}
}

func commandOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// ErrUnsupportedPlatform is returned where no service manager is known.
var ErrUnsupportedPlatform = errors.New("service control needs a Homebrew, snap or .deb/.rpm install of vybench")

// Status queries every service. It shells out, so call it off the UI thread.
func (s *Supervisor) Status() ([]Service, error) {
	switch s.platform {
	case PlatformBrew:
		return s.brewStatus()
	case PlatformSnap:
		return s.snapStatus()
	case PlatformNative:
		return s.systemdStatus()
	}
	return nil, ErrUnsupportedPlatform
}

// ControlSteps returns the commands that apply action ("start", "stop" or
// "restart") to the stack. Stop and restart leave the datastores alone.
func (s *Supervisor) ControlSteps(action string) ([]Step, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("unknown service action %q", action)
	}
	switch s.platform {
	case PlatformBrew:
		// Homebrew's own mariadb service is not started: it is a separate
		// server that takes port 3306 from the MariaDB vybench runs itself.
		if action != "start" {
			return []Step{{Cmd: exec.Command("brew", "services", action, s.name)}}, nil
		}
		return []Step{
			{Label: "Checking for a stale MariaDB socket", Fn: s.clearStaleSocket},
			{Label: "Starting " + s.name + " (MariaDB and Redis included)", Fn: s.startOrRestart},
		}, nil

	case PlatformSnap:
		svcs, err := s.snapStatus()
		if err != nil {
			return nil, err
		}
		var names []string
		for _, svc := range svcs {
			if svc.Detail == "disabled" {
				continue // off by design, e.g. watch outside developer mode
			}
			if action != "start" && isDatastore(svc.Name) {
				continue
			}
			names = append(names, svc.Name)
		}
		if len(names) == 0 {
			return nil, errors.New("no enabled services found")
		}
		return []Step{{Cmd: exec.Command("snapctl", append([]string{action}, names...)...)}}, nil

	case PlatformNative:
		return []Step{{Cmd: exec.Command("systemctl", action, "vybench.target")}}, nil
	}
	return nil, ErrUnsupportedPlatform
}

// startOrRestart starts the vybench service, or restarts it when it is
// already running without its MariaDB: the supervisor only starts the
// database when it starts, so a plain start would change nothing.
func (s *Supervisor) startOrRestart(log func(string)) error {
	action := "start"
	if !dialOK("unix", brewRunSocket()) || !dialOK("tcp", fmt.Sprintf("127.0.0.1:%d", BrewRedisPort())) {
		action = "restart"
	}
	log("$ brew services " + action + " " + s.name)
	out, err := exec.Command("brew", "services", action, s.name).CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			log(l)
		}
	}
	return err
}

func isDatastore(name string) bool {
	short := name[strings.LastIndex(name, ".")+1:]
	switch strings.TrimSuffix(short, ".service") {
	case "mariadb", "postgres", "postgresql", "redis", "redis-server":
		return true
	}
	return strings.HasPrefix(short, "postgresql@")
}

// ── Homebrew ────────────────────────────────────────────────────────────────

func (s *Supervisor) brewStatus() ([]Service, error) {
	out, err := s.output("brew", "services", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("brew services list: %w", err)
	}
	svcs, err := parseBrewServices(out, s.name)
	if err != nil {
		return nil, err
	}
	// The vybench service runs its own MariaDB and Redis on ports of their
	// own. Report those; Homebrew's mariadb and redis services are separate
	// servers vybench does not use, so their state says nothing here.
	own := func(name, label string, up bool, port int) Service {
		svc := Service{Name: name, Label: fmt.Sprintf("%s (port %d)", label, port), State: StateStopped}
		if up {
			svc.State = StateRunning
		}
		return svc
	}
	out2 := []Service{svcs[0],
		own("vybench-mariadb", "MariaDB", dialOK("unix", brewRunSocket()), BrewDBPort()),
		own("vybench-redis", "Redis", dialOK("tcp", fmt.Sprintf("127.0.0.1:%d", BrewRedisPort())), BrewRedisPort()),
	}
	for _, svc := range svcs[1:] {
		if strings.HasPrefix(svc.Name, "postgresql") {
			out2 = append(out2, svc)
		}
	}
	return out2, nil
}

func parseBrewServices(out []byte, instance string) ([]Service, error) {
	var rows []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing brew services output: %w", err)
	}
	status := make(map[string]string, len(rows))
	var postgres []string
	for _, r := range rows {
		status[r.Name] = r.Status
		if r.Name == "postgresql" || strings.HasPrefix(r.Name, "postgresql@") {
			postgres = append(postgres, r.Name)
		}
	}
	sort.Strings(postgres)

	wanted := []struct{ name, label string }{
		{instance, "Frappe (" + instance + ")"},
		{"mariadb", "MariaDB"},
		{"redis", "Redis"},
	}
	for _, pg := range postgres {
		wanted = append(wanted, struct{ name, label string }{pg, "PostgreSQL (" + pg + ")"})
	}
	var svcs []Service
	for _, w := range wanted {
		st, ok := status[w.name]
		svc := Service{Name: w.name, Label: w.label, State: brewState(st)}
		if !ok {
			svc.State, svc.Detail = StateStopped, "not installed"
		} else if st != "" && st != "started" && st != "none" && st != "stopped" {
			svc.Detail = st
		}
		svcs = append(svcs, svc)
	}
	return svcs, nil
}

func brewState(status string) ServiceState {
	switch status {
	case "started", "scheduled":
		return StateRunning
	case "stopped", "none", "":
		return StateStopped
	case "error":
		return StateFailed
	}
	return StateUnknown
}

// ── snap ────────────────────────────────────────────────────────────────────

var snapLabels = map[string]string{
	"web":          "Web (gunicorn)",
	"worker":       "Worker (default queue)",
	"worker-short": "Worker (short queue)",
	"worker-long":  "Worker (long queue)",
	"scheduler":    "Scheduler",
	"socketio":     "SocketIO",
	"watch":        "Asset watcher",
	"nginx":        "Nginx",
	"mariadb":      "MariaDB",
	"postgres":     "PostgreSQL",
	"redis":        "Redis",
}

func (s *Supervisor) snapStatus() ([]Service, error) {
	out, err := s.output("snapctl", "services")
	if err != nil {
		return nil, fmt.Errorf("snapctl services: %w", err)
	}
	return parseSnapServices(out), nil
}

// parseSnapServices reads `snapctl services`:
//
//	Service           Startup   Current   Notes
//	vybench.mariadb   enabled   active    -
//	vybench.watch     disabled  inactive  -
func parseSnapServices(out []byte) []Service {
	var svcs []Service
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[0] == "Service" {
			continue
		}
		app := f[0][strings.LastIndex(f[0], ".")+1:]
		label := snapLabels[app]
		if label == "" {
			label = app
		}
		svc := Service{Name: f[0], Label: label}
		switch f[2] {
		case "active":
			svc.State = StateRunning
		case "inactive":
			svc.State = StateStopped
		case "failed":
			svc.State = StateFailed
		default:
			svc.State = StateUnknown
		}
		if f[1] == "disabled" {
			svc.Detail = "disabled"
		}
		svcs = append(svcs, svc)
	}
	return svcs
}

// ── systemd (.deb / .rpm) ───────────────────────────────────────────────────

var systemdUnits = []struct{ unit, label string }{
	{"frappe-web.service", "Web (gunicorn)"},
	{"frappe-worker@default.service", "Worker (default queue)"},
	{"frappe-worker@short.service", "Worker (short queue)"},
	{"frappe-worker@long.service", "Worker (long queue)"},
	{"frappe-scheduler.service", "Scheduler"},
	{"frappe-socketio.service", "SocketIO"},
	{"mariadb.service", "MariaDB"},
	{"postgresql.service", "PostgreSQL"},
	{"redis-server.service", "Redis"},
	{"redis.service", "Redis"},
}

func (s *Supervisor) systemdStatus() ([]Service, error) {
	args := []string{"show", "--property=Id,LoadState,ActiveState,UnitFileState"}
	for _, u := range systemdUnits {
		args = append(args, u.unit)
	}
	out, err := s.output("systemctl", args...)
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("systemctl show: %w", err)
	}
	return parseSystemdShow(out), nil
}

// parseSystemdShow reads blank-line separated `systemctl show` blocks and
// skips units that are not installed.
func parseSystemdShow(out []byte) []Service {
	labels := make(map[string]string, len(systemdUnits))
	for _, u := range systemdUnits {
		labels[u.unit] = u.label
	}
	var svcs []Service
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		props := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		if props["Id"] == "" || props["LoadState"] == "not-found" {
			continue
		}
		svc := Service{Name: props["Id"], Label: labels[props["Id"]]}
		if svc.Label == "" {
			svc.Label = props["Id"]
		}
		switch props["ActiveState"] {
		case "active", "reloading", "activating":
			svc.State = StateRunning
		case "inactive", "deactivating":
			svc.State = StateStopped
		case "failed":
			svc.State = StateFailed
		}
		if props["UnitFileState"] == "disabled" {
			svc.Detail = "disabled"
		}
		svcs = append(svcs, svc)
	}
	return svcs
}

// StateLabel returns a human-readable label for a service state.
func StateLabel(st ServiceState) string {
	switch st {
	case StateRunning:
		return "● running"
	case StateStopped:
		return "○ stopped"
	case StateFailed:
		return "✖ failed"
	case StateConflict:
		return "⚠ in the way"
	}
	return "? unknown"
}
