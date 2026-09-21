package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestLogsModel(t *testing.T) {
	bench := t.TempDir()
	logDir := filepath.Join(bench, "logs")
	_ = os.MkdirAll(logDir, 0755)

	// Create test log files
	webLog := filepath.Join(logDir, "web.log")
	workerLog := filepath.Join(logDir, "worker.log")
	_ = os.WriteFile(webLog, []byte("line 1: INFO server started\nline 2: ERROR test failure\nline 3: WARN slow request\n"), 0644)
	_ = os.WriteFile(workerLog, []byte("worker ready\njob processing\n"), 0644)

	m := NewLogsModel(core.ActiveBench{Name: "main", Path: bench})
	m.SetSize(120, 30)

	// SetVisible triggers reading
	cmd := m.SetVisible(true)
	if cmd != nil {
		for _, msg := range run(cmd) {
			m, _ = m.Update(msg)
		}
	}

	view := ansi.Strip(m.View(120, 30))
	if !strings.Contains(view, "web.log") {
		t.Errorf("expected web.log in view, got: %s", view)
	}

	// Switch log file with 'right' / 'l'
	m, _ = m.Update(press("right"))
	m, _ = m.Update(press("left"))

	// Filter with '/'
	m, _ = m.Update(press("/"))
	if !m.filtering {
		t.Error("expected filtering to be active after '/'")
	}
	m = typeText(m, "ERROR")
	m, _ = m.Update(press("enter"))
	if m.filtering {
		t.Error("expected filtering to be inactive after enter")
	}

	// Toggle follow with 'f'
	m, _ = m.Update(press("f"))

	// Pause/Resume with 'p' and 'space'
	m, _ = m.Update(press("p"))
	m, _ = m.Update(press(" "))

	// Clear with 'c'
	m, _ = m.Update(press("c"))

	// Switch bench event
	m, _ = m.Update(BenchSwitchedMsg{Bench: core.ActiveBench{Name: "main", Path: bench}})

	// colorLogLine helper
	_ = colorLogLine("2026-01-01 [ERROR] something bad")
	_ = colorLogLine("2026-01-01 [WARN] something suspicious")
	_ = colorLogLine("2026-01-01 [INFO] all good")

	// Esc while filtering clears filter
	m, _ = m.Update(press("/"))
	m, _ = m.Update(press("esc"))
	if m.filtering {
		t.Error("expected filtering to be false after esc")
	}

	// Navigation keys in logs
	for _, k := range []string{"up", "down", "pgup", "pgdown", "home", "end", "g", "G"} {
		m, _ = m.Update(press(k))
	}

	// Rescan with r
	m, _ = m.Update(press("r"))

	// Empty log files with Snap journal hint
	emptyBench := t.TempDir()
	t.Setenv("SNAP", "/snap/vybench/current")
	emptyModel := NewLogsModel(core.ActiveBench{Name: "empty", Path: emptyBench})
	emptyModel.SetSize(120, 30)
	emptyModel.render()
	viewEmpty := ansi.Strip(emptyModel.View(120, 30))
	if !strings.Contains(viewEmpty, "No log files found") || !strings.Contains(viewEmpty, "journal") {
		t.Errorf("expected empty log and journal hint in view, got: %s", viewEmpty)
	}
	t.Setenv("SNAP", "")

	// Render with error on model with files
	m.err = "Permission denied"
	m.render()
	if !strings.Contains(ansi.Strip(m.View(120, 30)), "Permission denied") {
		t.Error("expected error message in view")
	}
}

func TestLogRankAndReadLog(t *testing.T) {
	// logRank
	if logRank("web.log") != 0 {
		t.Errorf("expected 0 for web.log, got %d", logRank("web.log"))
	}
	if logRank("worker.log") != 1 {
		t.Errorf("expected 1 for worker.log, got %d", logRank("worker.log"))
	}
	if logRank("custom.log") != 99 {
		t.Errorf("expected 99 for custom.log, got %d", logRank("custom.log"))
	}

	// readLog non-existent
	cmdErr := readLog(1, "/nonexistent/log.txt", 0, "")
	msgErr := cmdErr().(logChunkMsg)
	if msgErr.err == nil {
		t.Error("expected error reading nonexistent log")
	}

	// readLog with rotation (size < offset)
	tmp := t.TempDir()
	f := filepath.Join(tmp, "app.log")
	_ = os.WriteFile(f, []byte("line 1\nline 2\n"), 0644)
	cmdRot := readLog(1, f, 1000, "") // offset > size
	msgRot := cmdRot().(logChunkMsg)
	if len(msgRot.lines) == 0 {
		t.Error("expected lines after rotation reset")
	}

	// readLog already at EOF (size == offset)
	st, _ := os.Stat(f)
	cmdEOF := readLog(1, f, st.Size(), "")
	msgEOF := cmdEOF().(logChunkMsg)
	if len(msgEOF.lines) != 0 {
		t.Error("expected 0 lines when at EOF")
	}
}


