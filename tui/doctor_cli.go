package main

// `vybench doctor` — the command to reach for when a bench has stopped working
// and the error does not say why.
//
// It prints what it checked, not just what failed, because half of its value is
// ruling things out: knowing that all five apps import cleanly is what turns
// "bench is broken" into "the database is down".

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vyogotech/vybench/tui/core"
)

func runDoctorCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vybench doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fix := fs.Bool("fix", false, "repair what can be repaired, then re-check")
	all := fs.Bool("all", false, "check every registered bench, not only the active one")
	benchPath := fs.String("bench-path", "", "check this bench")
	offline := fs.Bool("offline", false, "skip the registry reachability check")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "vybench doctor:", err)
		fmt.Fprintln(stderr, "Usage: vybench doctor [--fix] [--all] [--bench-path DIR] [--offline]")
		return 2
	}

	mgr := core.NewManager()
	var paths []string
	switch {
	case *benchPath != "":
		paths = []string{*benchPath}
	case *all:
		paths = mgr.BenchPaths()
	default:
		paths = []string{mgr.ActiveBenchPath()}
	}
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "vybench doctor: no bench to check")
		return 1
	}

	worst := core.SevOK
	for i, p := range paths {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		if s := doctorOne(context.Background(), stdout, p, *fix, *offline); s > worst {
			worst = s
		}
	}
	if worst == core.SevError {
		return 1
	}
	return 0
}

func doctorOne(ctx context.Context, w io.Writer, benchPath string, fix, offline bool) core.Severity {
	fmt.Fprintf(w, "Checking %s\n\n", benchPath)
	opt := core.DoctorOptions{SkipNetwork: offline}
	report := core.RunDoctor(ctx, benchPath, opt)
	printReport(w, report)

	problems := report.Problems()
	if len(problems) == 0 {
		fmt.Fprintln(w, "\nNothing to fix.")
		return core.SevOK
	}

	if !fix {
		fixable := 0
		for _, f := range problems {
			if f.Fixable() {
				fixable++
			}
		}
		if fixable > 0 {
			fmt.Fprintf(w, "\n%d of %d can be repaired automatically: run 'vybench doctor --fix'.\n", fixable, len(problems))
		}
		return report.Worst()
	}

	fmt.Fprintln(w, "\nRepairing:")
	fixed, failed := report.Apply()
	for _, f := range report.Findings {
		switch {
		case f.Fixed:
			fmt.Fprintf(w, "  fixed    %s — %s\n", f.Title, f.FixLabel)
		case f.FixErr != nil:
			fmt.Fprintf(w, "  FAILED   %s — %v\n", f.Title, f.FixErr)
		case f.Severity != core.SevOK && !f.Fixable():
			fmt.Fprintf(w, "  manual   %s\n", f.Title)
		}
	}
	if fixed == 0 && failed == 0 {
		fmt.Fprintln(w, "  nothing could be repaired automatically")
	}

	fmt.Fprintln(w, "\nRe-checking:")
	after := core.RunDoctor(ctx, benchPath, opt)
	printReport(w, after)
	if after.Healthy() {
		fmt.Fprintln(w, "\nThis bench is healthy.")
	}
	return after.Worst()
}

func printReport(w io.Writer, r core.Report) {
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  [%-4s] %s\n", f.Severity, f.Title)
		if f.Severity == core.SevOK || f.Detail == "" {
			continue
		}
		for _, line := range strings.Split(f.Detail, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintf(w, "           %s\n", wrapDetail(line, 88, "           "))
			}
		}
		if f.FixLabel != "" {
			fmt.Fprintf(w, "           fix: %s\n", f.FixLabel)
		}
	}
}

// wrapDetail folds a long explanation to width, indenting continuations so the
// report stays readable in an 80–100 column terminal.
func wrapDetail(s string, width int, indent string) string {
	var b strings.Builder
	line := 0
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			b.WriteString(word)
			line = len(word)
		case line+1+len(word) > width:
			b.WriteString("\n" + indent + word)
			line = len(word)
		default:
			b.WriteString(" " + word)
			line += 1 + len(word)
		}
	}
	return b.String()
}
