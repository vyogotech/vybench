package core

// Platform detection and the commands vybench runs on the user's behalf.
//
// The TUI never talks to frappe or the service managers directly. It goes
// through the same wrapper the user would type (`vybench bench …` on Homebrew,
// `bench-wrapper` inside the snap), so PATH, database sockets and root
// credentials are prepared exactly as they are for a hand-typed command.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Platform identifies how vybench was installed.
type Platform int

const (
	PlatformUnknown Platform = iota
	PlatformBrew             // macOS Homebrew formula
	PlatformSnap             // Linux snap (strict confinement)
	PlatformNative           // Linux .deb/.rpm with systemd units
)

func (p Platform) String() string {
	switch p {
	case PlatformBrew:
		return "homebrew"
	case PlatformSnap:
		return "snap"
	case PlatformNative:
		return "native"
	}
	return "unknown"
}

// DetectPlatform inspects the environment the process was started from.
func DetectPlatform() Platform {
	if os.Getenv("SNAP") != "" {
		return PlatformSnap
	}
	if runtime.GOOS == "darwin" {
		return PlatformBrew
	}
	for _, unit := range []string{
		"/usr/lib/systemd/system/frappe-web.service",
		"/lib/systemd/system/frappe-web.service",
		"/etc/systemd/system/frappe-web.service",
	} {
		if fileExists(unit) {
			return PlatformNative
		}
	}
	return PlatformUnknown
}

// InstanceName is the formula or snap instance name, e.g. "vybench",
// "vybench-local" or "vypgbench". The wrappers export VYBENCH_NAME; when the
// TUI is started directly on macOS it picks whichever formula is installed.
func InstanceName() string {
	if n := os.Getenv("VYBENCH_NAME"); n != "" {
		return n
	}
	if n := os.Getenv("SNAP_INSTANCE_NAME"); n != "" {
		return n
	}
	if runtime.GOOS == "darwin" {
		opt := filepath.Join(BrewPrefix(), "opt")
		if !isDir(filepath.Join(opt, "vybench")) && (isDir(filepath.Join(opt, "vybench-local")) || isDir(filepath.Join(BrewPrefix(), "var/vybench-local"))) {
			return "vybench-local"
		}
	}
	return "vybench"
}

// BrewPrefix returns the Homebrew prefix: /opt/homebrew on Apple Silicon,
// /usr/local on Intel, unless HOMEBREW_PREFIX says otherwise.
func BrewPrefix() string {
	if p := os.Getenv("HOMEBREW_PREFIX"); p != "" {
		return p
	}
	if runtime.GOARCH == "arm64" {
		return "/opt/homebrew"
	}
	return "/usr/local"
}

func brewLibexec() string {
	if lib := os.Getenv("VYBENCH_LIBEXEC"); lib != "" {
		return lib
	}
	return filepath.Join(BrewPrefix(), "opt", InstanceName(), "libexec")
}

// PackagedBenchPath is the read-only bench shipped inside the package. A new
// bench on the same Frappe release links its apps/ and env/ instead of
// building its own. Returns "" when the platform has none.
func PackagedBenchPath() string {
	switch DetectPlatform() {
	case PlatformSnap:
		return filepath.Join(os.Getenv("SNAP"), "opt", "frappe-bench")
	case PlatformBrew:
		return filepath.Join(brewLibexec(), "frappe-bench")
	case PlatformNative:
		return "/opt/frappe-bench"
	}
	return ""
}

// vybenchCLI locates the Homebrew `vybench` wrapper script. The formula's own
// copy comes first: a keg that is not linked has no `vybench` on PATH.
func vybenchCLI() string {
	if p := filepath.Join(BrewPrefix(), "opt", InstanceName(), "bin", "vybench"); isExecutable(p) {
		return p
	}
	if p, err := exec.LookPath("vybench"); err == nil {
		return p
	}
	if p := filepath.Join(BrewPrefix(), "bin", "vybench"); isExecutable(p) {
		return p
	}
	return ""
}

// benchEnv returns the environment for a child process that must operate on
// benchPath. The TUI inherits BENCH_ROOT and FRAPPE_BENCH_ROOT from the wrapper
// that launched it; after a bench switch those point at the old bench, and
// frappe's get_bench_path() trusts FRAPPE_BENCH_ROOT, so all three are reset.
func benchEnv(benchPath string) []string {
	return append(os.Environ(),
		"VYBENCH_BENCH="+benchPath,
		"BENCH_ROOT="+benchPath,
		"FRAPPE_BENCH_ROOT="+benchPath,
	)
}

// benchProgram returns the program, and the arguments that precede bench's
// own, that run `bench` for benchPath. The platform wrapper is preferred so the
// command gets the same environment as a hand-typed one; VYBENCH_CLI, exported
// by the Homebrew wrapper, names a `vybench`-style CLI that takes `bench …`.
func benchProgram(benchPath string) (string, []string, error) {
	if DetectPlatform() == PlatformSnap {
		return filepath.Join(os.Getenv("SNAP"), "bin", "bench-wrapper"), nil, nil
	}
	if p := os.Getenv("VYBENCH_CLI"); p != "" && isExecutable(p) {
		return p, []string{"bench"}, nil
	}
	if DetectPlatform() == PlatformBrew {
		if cli := vybenchCLI(); cli != "" {
			return cli, []string{"bench"}, nil
		}
	}
	own := filepath.Join(benchPath, "env", "bin", "bench")
	if isExecutable(own) {
		return own, nil, nil
	}
	if p, err := exec.LookPath("bench"); err == nil {
		return p, nil, nil
	}
	return "", nil, fmt.Errorf("no bench command found: the vybench command is not installed and %s does not exist. "+
		"Install vybench (brew install vyogotech/tap/vybench), or put frappe-bench's bench on PATH (pipx install frappe-bench)", own)
}

// BenchCommand builds `bench <args…>` for benchPath.
func BenchCommand(benchPath string, args ...string) (*exec.Cmd, error) {
	prog, lead, err := benchProgram(benchPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(prog, append(lead, args...)...)
	cmd.Dir = benchPath
	cmd.Env = benchEnv(benchPath)
	return cmd, nil
}

// initPython returns the --python value `bench init` needs, or "" when the
// wrapper supplies one itself (the Homebrew wrapper injects $PYTHON).
func initPython() string {
	if DetectPlatform() == PlatformSnap {
		if p := filepath.Join(os.Getenv("SNAP"), "usr", "bin", "python3.14"); isExecutable(p) {
			return p
		}
	}
	return ""
}

// RestartHint is the command that makes running services pick up a bench switch.
func RestartHint() string {
	switch DetectPlatform() {
	case PlatformBrew:
		return "vybench restart"
	case PlatformSnap:
		return "sudo snap restart " + InstanceName()
	case PlatformNative:
		return "sudo systemctl restart vybench.target"
	}
	return "restart your bench processes"
}

// OpenURLCommand opens url in the desktop browser.
func OpenURLCommand(url string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url)
	}
	return exec.Command("xdg-open", url)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
