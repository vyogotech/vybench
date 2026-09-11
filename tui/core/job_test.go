package core

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, j *Job, timeout time.Duration) []string {
	t.Helper()
	var lines []string
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-j.Lines():
			if !ok {
				return lines
			}
			lines = append(lines, l)
		case <-deadline:
			t.Fatalf("job did not finish within %s; output so far: %q", timeout, lines)
		}
	}
}

func TestJobStreamsOutput(t *testing.T) {
	j := StartJob(JobSpec{Steps: []Step{
		{Label: "shell", Cmd: exec.Command("sh", "-c", `echo one; echo two >&2; printf 'three\rfour\r\nfive\n'; printf '\033[31mred\033[0m\tend'`)},
		{Label: "go", Fn: func(log func(string)) error { log("from go"); return nil }},
	}})
	lines := collect(t, j, 10*time.Second)
	if err := j.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"▶ shell", "$ sh -c", "one", "two", "three", "four", "five", "red    end", "▶ go", "from go"} {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, want) }) {
			t.Errorf("missing %q in %q", want, lines)
		}
	}
}

func TestJobStopsAtFailure(t *testing.T) {
	failed := false
	ranAfter := false
	j := StartJob(JobSpec{
		Steps: []Step{
			{Cmd: exec.Command("sh", "-c", "echo boom; exit 3")},
			{Fn: func(func(string)) error { ranAfter = true; return nil }},
		},
		OnFailure: func() { failed = true },
	})
	collect(t, j, 10*time.Second)
	if err := j.Err(); err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Errorf("err = %v", err)
	}
	if ranAfter || !failed {
		t.Errorf("ranAfter=%v failed=%v", ranAfter, failed)
	}
}

func TestJobCancelKillsProcessTree(t *testing.T) {
	failed := false
	j := StartJob(JobSpec{
		Steps:     []Step{{Cmd: exec.Command("sh", "-c", "sleep 30 & sleep 30; echo unreachable")}},
		OnFailure: func() { failed = true },
	})
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	j.Cancel()
	lines := collect(t, j, 10*time.Second)
	if !errors.Is(j.Err(), ErrCancelled) || !failed {
		t.Errorf("err=%v failed=%v", j.Err(), failed)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
	if slices.Contains(lines, "unreachable") {
		t.Error("the command kept running after cancel")
	}
}

// A child that prompts (getpass opens /dev/tty) must fail, not hang the TUI.
func TestJobChildHasNoTerminal(t *testing.T) {
	j := StartJob(JobSpec{Steps: []Step{{Cmd: exec.Command("sh", "-c", "read x < /dev/tty")}}})
	collect(t, j, 10*time.Second)
	if j.Err() == nil {
		t.Error("reading /dev/tty succeeded; the child still has a controlling terminal")
	}
}

func TestDisplayCommandMasksPasswords(t *testing.T) {
	cmd := exec.Command("/usr/bin/bench", "new-site", "a.localhost", "--admin-password", "hunter2",
		"--db-root-password=s3cret", "--db-type", "mariadb")
	got := DisplayCommand(cmd)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "s3cret") {
		t.Errorf("password leaked: %s", got)
	}
	if !strings.HasPrefix(got, "bench new-site a.localhost --admin-password **** --db-root-password=****") {
		t.Errorf("DisplayCommand = %s", got)
	}
}

func TestScanLinesSplitsLongLines(t *testing.T) {
	data := []byte(strings.Repeat("x", maxLine+10))
	adv, tok, _ := scanLines(data, false)
	if adv != len(data) || len(tok) != len(data) {
		t.Errorf("long line: advance %d token %d", adv, len(tok))
	}
	if adv, _, _ := scanLines([]byte("abc\r"), false); adv != 0 {
		t.Error("a trailing \\r must wait for a possible \\n")
	}
}
