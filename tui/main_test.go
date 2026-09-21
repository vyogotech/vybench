package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vyogotech/vybench/tui/core"
)

func TestMainHelpers(t *testing.T) {
	// isDir
	tmp := t.TempDir()
	if !isDir(tmp) {
		t.Errorf("expected isDir to be true for %s", tmp)
	}
	f := filepath.Join(tmp, "file.txt")
	_ = os.WriteFile(f, []byte("x"), 0644)
	if isDir(f) {
		t.Errorf("expected isDir to be false for file")
	}
	if isDir(filepath.Join(tmp, "nonexistent")) {
		t.Errorf("expected isDir to be false for nonexistent")
	}

	// isTerminal
	// tmp file is not a terminal
	tmpFile, _ := os.Open(f)
	defer tmpFile.Close()
	_ = isTerminal(tmpFile)

	// usageError
	u := usageError("test usage")
	if u.Error() != "usage: test usage" {
		t.Errorf("unexpected usageError string: %s", u.Error())
	}

	// flagError
	err := flagError(errors.New("invalid flag"), "command [options]")
	if err.Error() != "usage: command [options]" {
		t.Errorf("unexpected flagError result: %v", err)
	}

	// printBenchHelp
	var buf bytes.Buffer
	printBenchHelp(&buf)
	if buf.Len() == 0 {
		t.Error("expected non-empty help output")
	}

	// postgresHint
	hint := postgresHint()
	if hint == "" {
		t.Error("expected non-empty postgresHint")
	}

	// parseInterleaved
	fs := newFlagSet("test")
	val := fs.String("flag", "", "test flag")
	pos, err := parseInterleaved(fs, []string{"pos1", "--flag=hello", "pos2"})
	if err != nil {
		t.Fatalf("parseInterleaved failed: %v", err)
	}
	if *val != "hello" || len(pos) != 2 || pos[0] != "pos1" || pos[1] != "pos2" {
		t.Errorf("parseInterleaved got pos=%v, flag=%s", pos, *val)
	}
}

func TestRunSteps(t *testing.T) {
	// Success with Fn and Cmd
	ranFn := false
	spec := core.JobSpec{
		Steps: []core.Step{
			{
				Label: "fn step",
				Fn: func(log func(string)) error {
					ranFn = true
					log("doing something")
					return nil
				},
			},
			{
				Label: "cmd step",
				Cmd:   exec.Command("true"),
			},
		},
	}
	if err := runSteps(spec); err != nil {
		t.Fatalf("runSteps failed: %v", err)
	}
	if !ranFn {
		t.Error("fn step did not run")
	}

	// Failure with OnFailure callback
	failed := false
	failSpec := core.JobSpec{
		OnFailure: func() {
			failed = true
		},
		Steps: []core.Step{
			{
				Label: "fail step",
				Fn: func(log func(string)) error {
					return errors.New("step error")
				},
			},
		},
	}
	if err := runSteps(failSpec); err == nil {
		t.Fatal("expected runSteps to fail")
	}
	if !failed {
		t.Error("OnFailure was not invoked")
	}
}

func TestRunBenchCLI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("VYBENCH_DIR", tmpDir)
	t.Setenv("SNAP_DATA", tmpDir)
	t.Setenv("SNAP_COMMON", tmpDir)

	// Help commands
	if code := runBenchCLI([]string{}); code != 0 {
		t.Errorf("expected 0 for empty args, got %d", code)
	}
	if code := runBenchCLI([]string{"help"}); code != 0 {
		t.Errorf("expected 0 for help, got %d", code)
	}
	if code := runBenchCLI([]string{"--help"}); code != 0 {
		t.Errorf("expected 0 for --help, got %d", code)
	}

	// Unknown command
	if code := runBenchCLI([]string{"invalid"}); code != 2 {
		t.Errorf("expected 2 for invalid, got %d", code)
	}

	// list (empty)
	if code := runBenchCLI([]string{"list"}); code != 0 {
		t.Errorf("expected 0 for list, got %d", code)
	}

	// current
	if code := runBenchCLI([]string{"current"}); code != 0 {
		t.Errorf("expected 0 for current, got %d", code)
	}

	// switch with wrong args
	if code := runBenchCLI([]string{"switch"}); code != 2 {
		t.Errorf("expected 2 for switch without args, got %d", code)
	}
	// switch to unknown
	if code := runBenchCLI([]string{"switch", "missing"}); code != 1 {
		t.Errorf("expected 1 for switch missing, got %d", code)
	}

	// attach with wrong args
	if code := runBenchCLI([]string{"attach"}); code != 2 {
		t.Errorf("expected 2 for attach without args, got %d", code)
	}

	// drop with wrong args
	if code := runBenchCLI([]string{"drop"}); code != 2 {
		t.Errorf("expected 2 for drop without args, got %d", code)
	}
	// drop unknown
	if code := runBenchCLI([]string{"drop", "missing"}); code != 1 {
		t.Errorf("expected 1 for drop missing, got %d", code)
	}

	// new with wrong args
	if code := runBenchCLI([]string{"new"}); code != 2 {
		t.Errorf("expected 2 for new without args, got %d", code)
	}

	// Create a bench directory to test attach, switch, list, and drop
	benchDir := filepath.Join(tmpDir, "mybench")
	_ = os.MkdirAll(filepath.Join(benchDir, "sites"), 0755)
	_ = os.WriteFile(filepath.Join(benchDir, "sites", "common_site_config.json"), []byte("{}"), 0644)

	// attach valid
	if code := runBenchCLI([]string{"attach", "testbench", benchDir, "--db", "mariadb"}); code != 0 {
		t.Errorf("expected 0 for attach, got %d", code)
	}

	// list with bench registered
	if code := runBenchCLI([]string{"list"}); code != 0 {
		t.Errorf("expected 0 for list with benches, got %d", code)
	}

	// switch to valid
	if code := runBenchCLI([]string{"switch", "testbench"}); code != 0 {
		t.Errorf("expected 0 for switch to testbench, got %d", code)
	}

	// current with active bench
	if code := runBenchCLI([]string{"current"}); code != 0 {
		t.Errorf("expected 0 for current with active, got %d", code)
	}

	benchDir2 := filepath.Join(tmpDir, "mybench2")
	_ = os.MkdirAll(filepath.Join(benchDir2, "sites"), 0755)
	_ = os.WriteFile(filepath.Join(benchDir2, "sites", "common_site_config.json"), []byte("{}"), 0644)
	if code := runBenchCLI([]string{"attach", "testbench2", benchDir2, "--db", "mariadb"}); code != 0 {
		t.Errorf("expected 0 for attach bench2, got %d", code)
	}
	if code := runBenchCLI([]string{"switch", "testbench2"}); code != 0 {
		t.Errorf("expected 0 for switch to testbench2, got %d", code)
	}

	// cmdCurrent and cmdList when pinned
	t.Setenv("VYBENCH_BENCH", benchDir2)
	if code := runBenchCLI([]string{"current"}); code != 0 {
		t.Errorf("expected 0 for current pinned, got %d", code)
	}
	if code := runBenchCLI([]string{"list"}); code != 0 {
		t.Errorf("expected 0 for list pinned, got %d", code)
	}
	_ = os.Unsetenv("VYBENCH_BENCH")

	// cmdNew error (e.g. invalid DB engine)
	if code := runBenchCLI([]string{"new", "badbench", "--db", "invalid"}); code == 0 {
		t.Errorf("expected non-zero for invalid db engine")
	}

	// drop valid non-active bench
	if code := runBenchCLI([]string{"drop", "testbench"}); code != 0 {
		t.Errorf("expected 0 for drop testbench, got %d", code)
	}
}

func TestRunBenchCLINewVariants(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("VYBENCH_DIR", tmpDir)
	t.Setenv("VYBENCH_VAR", filepath.Join(tmpDir, "var"))
	t.Setenv("SNAP_DATA", tmpDir)
	t.Setenv("SNAP_COMMON", filepath.Join(tmpDir, "var"))
	t.Setenv("SNAP", tmpDir)

	// Set up packaged bench so PackagedFrappeVersion returns "16.1.0"
	packagedFrappe := filepath.Join(tmpDir, "opt", "frappe-bench", "apps", "frappe", "frappe")
	if err := os.MkdirAll(packagedFrappe, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagedFrappe, "__init__.py"), []byte("__version__ = '16.1.0'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create linked bench with --switch=false
	if code := runBenchCLI([]string{"new", "linked1", "--switch=false"}); code != 0 {
		t.Errorf("expected 0 for new linked1, got %d", code)
	}

	// Create linked bench with postgres and --switch=true (exercises postgresHint)
	if code := runBenchCLI([]string{"new", "linkedpg", "--db", "postgres", "--switch=true"}); code != 0 {
		t.Errorf("expected 0 for new linkedpg, got %d", code)
	}

	// Create bench with already existing name (error path)
	if code := runBenchCLI([]string{"new", "linked1"}); code != 1 {
		t.Errorf("expected 1 for duplicate bench name, got %d", code)
	}

	// NeedsInit branch using runStepsFunc hook
	origRunSteps := runStepsFunc
	defer func() { runStepsFunc = origRunSteps }()
	runStepsFunc = func(spec core.JobSpec) error { return nil }

	if code := runBenchCLI([]string{"new", "initbench", "--version", "15", "--switch=true"}); code != 0 {
		t.Errorf("expected 0 for new initbench with mocked runSteps, got %d", code)
	}

	// NeedsInit with runSteps error
	runStepsFunc = func(spec core.JobSpec) error { return errors.New("init step failed") }
	if code := runBenchCLI([]string{"new", "failbench", "--version", "15"}); code != 1 {
		t.Errorf("expected 1 for new failbench with error, got %d", code)
	}

	// NeedsInit with core.ErrCancelled -> returns 130
	runStepsFunc = func(spec core.JobSpec) error { return core.ErrCancelled }
	if code := runBenchCLI([]string{"new", "cancelbench", "--version", "15"}); code != 130 {
		t.Errorf("expected 130 for cancelled bench init, got %d", code)
	}

	// Attach error
	if code := runBenchCLI([]string{"attach", "badbench", "/nonexistent/path"}); code != 1 {
		t.Errorf("expected 1 for attach non-existent path, got %d", code)
	}

	// Current unregistered bench
	unregBench := filepath.Join(tmpDir, "unreg")
	_ = os.MkdirAll(filepath.Join(unregBench, "sites"), 0755)
	t.Setenv("VYBENCH_BENCH", unregBench)
	if code := runBenchCLI([]string{"current"}); code != 0 {
		t.Errorf("expected 0 for current unregistered, got %d", code)
	}

	// Switch when pinned to a different bench
	if code := runBenchCLI([]string{"switch", "linked1"}); code != 0 {
		t.Errorf("expected 0 for switch when pinned, got %d", code)
	}
	_ = os.Unsetenv("VYBENCH_BENCH")
}

func TestRunMain(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("VYBENCH_DIR", tmpDir)
	t.Setenv("VYBENCH_VAR", filepath.Join(tmpDir, "var"))
	t.Setenv("SNAP_DATA", tmpDir)
	t.Setenv("SNAP_COMMON", filepath.Join(tmpDir, "var"))

	// 1. vybench bench list
	if code := runMain([]string{"vybench", "bench", "list"}, os.Stdin, os.Stdout, os.Stderr); code != 0 {
		t.Errorf("expected 0 for runMain bench list, got %d", code)
	}

	// 2. vybench-tui --version
	if code := runMain([]string{"vybench-tui", "--version"}, os.Stdin, os.Stdout, os.Stderr); code != 0 {
		t.Errorf("expected 0 for runMain --version, got %d", code)
	}

	// 3. vybench-tui --invalid-flag
	if code := runMain([]string{"vybench-tui", "--invalid-flag"}, os.Stdin, os.Stdout, os.Stderr); code != 2 {
		t.Errorf("expected 2 for runMain with invalid flag, got %d", code)
	}

	// 4. vybench-tui --bench-path /nonexistent
	if code := runMain([]string{"vybench-tui", "--bench-path", filepath.Join(tmpDir, "missing")}, os.Stdin, os.Stdout, os.Stderr); code != 2 {
		t.Errorf("expected 2 for runMain with non-existent bench path, got %d", code)
	}

	// 5. vybench-tui --bench-path not-a-bench (dir without sites/)
	if code := runMain([]string{"vybench-tui", "--bench-path", tmpDir}, os.Stdin, os.Stdout, os.Stderr); code != 2 {
		t.Errorf("expected 2 for runMain with dir lacking sites/, got %d", code)
	}

	// 6. vybench-tui --bench-path valid-bench (dir with sites/)
	benchDir := filepath.Join(tmpDir, "valid-bench")
	_ = os.MkdirAll(filepath.Join(benchDir, "sites"), 0755)
	// Expect 1 because non-interactive test environment has no terminal
	if code := runMain([]string{"vybench-tui", "--bench-path", benchDir}, os.Stdin, os.Stdout, os.Stderr); code != 1 {
		t.Errorf("expected 1 for runMain without terminal, got %d", code)
	}

	// 7. vybench-tui default invocation without terminal
	if code := runMain([]string{"vybench-tui"}, os.Stdin, os.Stdout, os.Stderr); code != 1 {
		t.Errorf("expected 1 for runMain without terminal, got %d", code)
	}
}

func TestMainEntrypoint(t *testing.T) {
	if os.Getenv("GO_WANT_MAIN_RUN") == "1" {
		os.Args = []string{"vybench", "bench", "list"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainEntrypoint")
	cmd.Env = append(os.Environ(), "GO_WANT_MAIN_RUN=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("main process failed: %v\nOutput: %s", err, string(out))
	}
}

