// vybench-tui — interactive TUI and multi-bench CLI for vybench.
//
// Usage:
//
//	vybench tui                                   launch the interactive TUI
//	vybench bench list                            list registered benches
//	vybench bench current                         show the active bench
//	vybench bench switch <name>                   switch the active bench
//	vybench bench new <name> [--db E] [--version V] [--python P] [--switch=false]
//	vybench bench attach <name> <path> [--db E]   register an existing bench
//	vybench bench drop <name>                     remove a bench from the registry
//	vybench doctor [--fix] [--all]                diagnose, and repair, a broken bench
//	vybench-tui --bench-path DIR                  open the TUI on another bench
//	vybench-tui --version                         print the version
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/vyogotech/vybench/tui/core"
	"github.com/vyogotech/vybench/tui/ui"
)

// version is set at build time: -ldflags "-X main.version=16.1.0".
var version = "dev"

func main() {
	os.Exit(runMain(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func runMain(args []string, stdin, stdout, stderr *os.File) int {
	core.EnsureSnapDaemonGroup()

	if len(args) > 1 && args[1] == "bench" {
		return runBenchCLI(args[2:])
	}
	if len(args) > 1 && args[1] == "doctor" {
		return runDoctorCLI(args[2:], stdout, stderr)
	}

	fs := flag.NewFlagSet("vybench-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	benchPath := fs.String("bench-path", "", "open the TUI on this bench instead of the active one")
	showVer := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *showVer {
		fmt.Fprintln(stdout, "vybench-tui", version)
		return 0
	}
	if *benchPath != "" {
		abs, err := filepath.Abs(*benchPath)
		if err == nil && !isDir(filepath.Join(abs, "sites")) {
			err = fmt.Errorf("%s is not a bench (it has no sites/ directory)", abs)
		}
		if err != nil {
			fmt.Fprintln(stderr, "vybench-tui:", err)
			return 2
		}
		_ = os.Setenv("VYBENCH_BENCH", abs)
	}
	if !isTerminalFunc(stdin) || !isTerminalFunc(stdout) {
		fmt.Fprintln(stderr, "vybench-tui: the interactive TUI needs a terminal; use 'vybench bench list' and friends in scripts")
		return 1
	}

	app := ui.NewApp(core.NewManager(), core.NewSupervisor(), core.NewFPMClient())
	final, err := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
	if a, ok := final.(ui.App); ok {
		a.Shutdown()
	}
	if err != nil {
		fmt.Fprintln(stderr, "vybench-tui:", err)
		return 1
	}
	return 0
}

func runBenchCLI(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printBenchHelp(os.Stdout)
		return 0
	}
	mgr := core.NewManager()
	var err error
	switch args[0] {
	case "list":
		err = cmdList(mgr)
	case "current":
		err = cmdCurrent(mgr)
	case "switch":
		err = cmdSwitch(mgr, args[1:])
	case "new":
		err = cmdNew(mgr, args[1:])
	case "attach":
		err = cmdAttach(mgr, args[1:])
	case "drop":
		err = cmdDrop(mgr, args[1:])
	case "doctor":
		return runDoctorCLI(args[1:], os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "vybench: unknown bench command %q\n\n", args[0])
		printBenchHelp(os.Stderr)
		return 2
	}
	var usage usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage):
		fmt.Fprintln(os.Stderr, "Usage: vybench bench "+string(usage))
		return 2
	case errors.Is(err, core.ErrCancelled):
		fmt.Fprintln(os.Stderr, "vybench: cancelled")
		return 130
	}
	fmt.Fprintln(os.Stderr, "vybench:", err)
	return 1
}

type usageError string

func (u usageError) Error() string { return "usage: " + string(u) }

func printBenchHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: vybench bench <command> [options]

Commands:
  list                              List registered benches (* marks the active one)
  current                           Show the active bench and why it is active
  switch <name>                     Make <name> the active bench for every shell and the services
  new <name> [options]              Create a bench
      --db mariadb|postgres           database engine (default mariadb)
      --version VER                   Frappe version: 16, 15, develop, v16.3.0, a branch
                                      (default: the packaged release, linked in seconds;
                                      any other version is built with 'bench init')
      --python PATH                   Python for 'bench init' (default: the bundled one)
      --switch=false                  do not make the new bench active
  attach <name> <path> [--db E]     Register an existing bench directory
  drop <name>                       Remove a bench from the registry (files are kept)
  doctor [--fix] [--all]            Diagnose the bench and, with --fix, repair what it can
`)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported once, with our usage line
	return fs
}

// flagError reports a flag parsing error (if any) and returns the usage.
func flagError(err error, usage string) error {
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "vybench:", err)
	}
	return usageError(usage)
}

// parseInterleaved parses flags that may appear before, between or after
// positional arguments, and returns the positionals.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdList(mgr *core.Manager) error {
	benches, err := mgr.ListBenches()
	if err != nil {
		return err
	}
	if len(benches) == 0 {
		fmt.Println("No benches registered. Create one with 'vybench bench new <name>'.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tDB\tFRAPPE\tSITES\tPATH")
	for _, b := range benches {
		marker := "  "
		if b.IsActive {
			marker = "* "
		}
		path := b.Entry.Path
		if b.Missing {
			path += "  (missing)"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%d\t%s\n", marker, b.Name, b.Entry.DBEngine,
			core.FormatFrappeVersion(b.Entry.FrappeVersion), len(b.Sites), path)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if cur := mgr.ResolveActive(); cur.Pinned() {
		fmt.Printf("\nThis shell uses %s (%s).\n", cur.Path, cur.PinReason())
	}
	return nil
}

func cmdCurrent(mgr *core.Manager) error {
	cur := mgr.ResolveActive()
	fmt.Printf("%s  Frappe %s · %s · %s\n", cur.Name, core.FormatFrappeVersion(cur.Entry.FrappeVersion), cur.Entry.DBEngine, cur.Path)
	if !cur.Registered {
		fmt.Printf("(not registered — 'vybench bench attach <name> %s' adds it)\n", cur.Path)
	}
	if cur.Pinned() {
		fmt.Printf("(%s)\n", cur.PinReason())
	}
	return nil
}

func cmdSwitch(mgr *core.Manager, args []string) error {
	if len(args) != 1 {
		return usageError("switch <name>")
	}
	before := mgr.ResolveActive()
	if err := mgr.SwitchBench(args[0]); err != nil {
		return err
	}
	b, err := mgr.Bench(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("Switched the active bench to '%s' (%s).\n", b.Name, b.Path)
	fmt.Printf("Running services still serve the previous bench until restarted: %s\n", core.RestartHint())
	if before.Pinned() && !core.SamePath(before.Path, b.Path) {
		fmt.Printf("This shell stays on %s: it is %s.\n", before.Path, before.PinReason())
	}
	return nil
}

func cmdNew(mgr *core.Manager, args []string) error {
	fs := newFlagSet("bench new")
	db := fs.String("db", "mariadb", "database engine: mariadb or postgres")
	ver := fs.String("version", "", "Frappe version")
	python := fs.String("python", "", "Python for bench init")
	switchTo := fs.Bool("switch", true, "make the new bench active")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		return flagError(err, "new <name> [--db mariadb|postgres] [--version VER] [--python PATH] [--switch=false]")
	}

	plan, err := mgr.PlanBench(core.NewBenchOptions{
		Name: pos[0], DBEngine: *db, FrappeVersion: *ver, Python: *python, SwitchToNew: *switchTo,
	})
	if err != nil {
		return err
	}
	if plan.DBEngine == "postgres" && !core.DatabaseReachable("", "postgres") {
		fmt.Fprintln(os.Stderr, "Warning: nothing is listening on 127.0.0.1:5432. "+postgresHint())
	}

	if !plan.NeedsInit {
		if err := mgr.CreateLinked(plan); err != nil {
			return err
		}
		fmt.Printf("Created bench '%s' (%s, Frappe %s) at %s.\n", plan.Name, plan.DBEngine, core.FormatFrappeVersion(plan.FrappeVersion), plan.Path)
	} else {
		if plan.PackagedVersion != "" {
			fmt.Printf("Frappe %s is not the packaged release (%s), so it is built with 'bench init'.\n", plan.FrappeVersion, plan.PackagedVersion)
		}
		fmt.Println("This downloads Frappe and its dependencies and takes several minutes. It needs a Python and Node that the release supports.")
		fmt.Println()
		spec, err := mgr.InitBenchSpec(plan, mgr.ActiveBenchPath())
		if err != nil {
			return err
		}
		if err := runStepsFunc(spec); err != nil {
			return err
		}
		fmt.Printf("\nCreated bench '%s' (%s, Frappe %s) at %s.\n", plan.Name, plan.DBEngine, plan.Branch, plan.Path)
	}
	if plan.SwitchToNew {
		fmt.Printf("It is now the active bench. Restart running services to serve it: %s\n", core.RestartHint())
	}
	return nil
}

func postgresHint() string {
	switch core.DetectPlatform() {
	case core.PlatformBrew:
		return "Install and start PostgreSQL before creating sites on this bench: brew install postgresql@16 && brew services start postgresql@16"
	case core.PlatformSnap:
		return "The vybench snap ships MariaDB only; PostgreSQL benches need the separate vypgbench snap or a PostgreSQL server on this port."
	}
	return "Install and start PostgreSQL 16 before creating sites on this bench."
}

var runStepsFunc = runSteps

// runSteps runs a job in the foreground with the terminal attached, so bench
// shows its own progress and Ctrl+C reaches it directly.
func runSteps(spec core.JobSpec) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	for _, s := range spec.Steps {
		if s.Label != "" {
			fmt.Println("==> " + s.Label)
		}
		var err error
		if s.Cmd != nil {
			fmt.Println("$ " + core.DisplayCommand(s.Cmd))
			s.Cmd.Stdin, s.Cmd.Stdout, s.Cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			err = s.Cmd.Run()
		} else if s.Fn != nil {
			err = s.Fn(func(line string) { fmt.Println(line) })
		}
		select {
		case <-sig:
			err = core.ErrCancelled
		default:
		}
		if err != nil {
			if spec.OnFailure != nil {
				spec.OnFailure()
			}
			return err
		}
	}
	return nil
}

func cmdAttach(mgr *core.Manager, args []string) error {
	fs := newFlagSet("bench attach")
	db := fs.String("db", "", "database engine (default: read from the bench)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		return flagError(err, "attach <name> <path> [--db mariadb|postgres]")
	}
	if err := mgr.AttachBench(pos[0], pos[1], *db, ""); err != nil {
		return err
	}
	fmt.Printf("Attached '%s'. Switch to it with 'vybench bench switch %s'.\n", pos[0], pos[0])
	return nil
}

func cmdDrop(mgr *core.Manager, args []string) error {
	if len(args) != 1 {
		return usageError("drop <name>")
	}
	if err := mgr.DropBench(args[0]); err != nil {
		return err
	}
	fmt.Printf("Removed '%s' from the registry. Its files were kept.\n", args[0])
	return nil
}

var isTerminalFunc = isTerminal

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
