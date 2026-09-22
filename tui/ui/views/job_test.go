package views

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestJobModelLifecycleAndViews(t *testing.T) {
	spec := core.JobSpec{
		Steps: []core.Step{
			{
				Label: "step 1",
				Fn: func(log func(string)) error {
					log("running step 1")
					return nil
				},
			},
		},
	}

	runMsg := RunJobMsg{
		Title:    "Test Job",
		Spec:     spec,
		Success:  "Job finished perfectly",
		FailHint: "check logs",
	}

	m, _ := NewJob(runMsg)
	m.SetSize(80, 24)

	if !m.Active() || !m.Running() {
		t.Fatal("expected job to be active and running")
	}

	// Spinner tick
	m, _ = m.Update(spinner.TickMsg{})

	// Append lines
	m, _ = m.Update(jobLinesMsg{
		id:    m.id,
		lines: []string{"▶ step 1", "$ command line", "normal line", "warn: something minor", "error: something broke"},
	})

	// Mismatched id lines should be ignored
	m, _ = m.Update(jobLinesMsg{
		id:    m.id + 999,
		lines: []string{"ignored line"},
	})

	// View while running
	viewRunning := ansi.Strip(m.View())
	if !strings.Contains(viewRunning, "Test Job") || !strings.Contains(viewRunning, "running…") {
		t.Errorf("view running unexpected: %s", viewRunning)
	}

	// Navigation keys
	for _, k := range []string{"up", "k", "pgup", "home", "g", "down", "j", "pgdown", "end", "G"} {
		m, _ = m.Update(press(k))
	}

	// Cancel via x key
	m, _ = m.Update(press("x"))
	m.Cancel()

	// Done success
	mDone, _ := m.Update(jobDoneMsg{id: m.id, err: nil})
	if !mDone.done || mDone.Running() {
		t.Error("job should be done")
	}
	viewDone := ansi.Strip(mDone.View())
	if !strings.Contains(viewDone, "done") {
		t.Errorf("view done unexpected: %s", viewDone)
	}

	// Done cancelled
	mCancel, _ := m.Update(jobDoneMsg{id: m.id, err: core.ErrCancelled})
	viewCancel := ansi.Strip(mCancel.View())
	if !strings.Contains(viewCancel, "cancelled") {
		t.Errorf("view cancel unexpected: %s", viewCancel)
	}

	// Done with error and diagnosis
	mErr, _ := m.Update(jobLinesMsg{
		id:    m.id,
		lines: []string{"Error: Can't connect to local server through socket '/tmp/mysql.sock'"},
	})
	mErr, _ = mErr.Update(jobDoneMsg{id: m.id, err: errors.New("socket error")})
	viewErr := ansi.Strip(mErr.View())
	if !strings.Contains(viewErr, "socket error") {
		t.Errorf("view err unexpected: %s", viewErr)
	}

	// Close on esc or enter when done
	closed, _ := mDone.Update(press("esc"))
	if closed.Active() {
		t.Error("expected job to close on esc")
	}
	closed2, _ := mDone.Update(press("enter"))
	if closed2.Active() {
		t.Error("expected job to close on enter")
	}

	// colorOutput
	for _, l := range []string{
		"▶ Step Label",
		"$ Command line",
		"ERROR: failed",
		"Traceback (most recent call last):",
		"Warn: heads up",
		"Clean standard text",
	} {
		_ = colorOutput(l)
	}

	// firstNonEmpty
	if v := firstNonEmpty("", "", "first", "second"); v != "first" {
		t.Errorf("expected first, got %s", v)
	}
	if v := firstNonEmpty("", ""); v != "" {
		t.Errorf("expected empty string, got %s", v)
	}
}

func TestJobErrBeforeStart(t *testing.T) {
	if err := (JobModel{}).Err(); err != nil {
		t.Fatal(err)
	}
}
