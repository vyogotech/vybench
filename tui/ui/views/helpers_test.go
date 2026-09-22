package views

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/vyogotech/vybench/tui/core"
)

func TestHelpersExtended(t *testing.T) {
	// Overlay & Fit
	base := "Line 1\nLine 2\nLine 3"
	top := "MODAL"
	over := Overlay(base, top, 20, 5)
	if !strings.Contains(over, "MODAL") {
		t.Errorf("Overlay missing top modal: %s", over)
	}

	fitted := Fit("Long single line of text", 10, 2)
	lines := strings.Split(fitted, "\n")
	if len(lines) != 2 {
		t.Errorf("Fit expected 2 lines, got %d", len(lines))
	}
	if ansi.StringWidth(lines[0]) > 10 {
		t.Errorf("Fit line 1 width %d > 10", ansi.StringWidth(lines[0]))
	}

	// scrollHint
	if h := scrollHint(0, 10, 5); h != "" {
		t.Errorf("expected empty scroll hint when all visible, got %s", h)
	}
	if h := scrollHint(0, 5, 20); h == "" {
		t.Error("expected non-empty scroll hint when list scrolls")
	}
	if h := scrollHint(5, 15, 50); !strings.Contains(h, "6–15 of 50") {
		t.Errorf("expected 6–15 of 50, got %s", h)
	}

	// truncateLeft
	if t1 := truncateLeft("short", 10); t1 != "short" {
		t.Errorf("expected short, got %s", t1)
	}
	if t2 := truncateLeft("/very/long/path/to/file", 10); !strings.HasPrefix(t2, "…") {
		t.Errorf("expected truncated prefix, got %s", t2)
	}
	if t3 := truncateLeft("abc", 1); t3 != "abc" {
		t.Errorf("expected abc when width < 2, got %s", t3)
	}

	// Overlay too tall
	tallTop := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10"
	overTall := Overlay("b1\nb2\nb3\nb4\nb5\nb6", tallTop, 30, 6)
	if overTall == "" {
		t.Error("expected non-empty overlay for tall top")
	}

	// panel min clamping
	p := panel(1, 1, "test")
	if p == "" {
		t.Error("expected non-empty panel")
	}

	// helpBar, errorText, dialog
	hb := helpBar(40, "q", "quit", "r", "reload")
	if hb == "" {
		t.Error("expected non-empty helpBar")
	}
	et := errorText("invalid input occurred")
	if !strings.Contains(et, "invalid input") {
		t.Errorf("unexpected errorText: %s", et)
	}
	d := dialog("Dialog Title", []string{"r1", "r2"}, "[q] quit")
	if !strings.Contains(d, "Dialog Title") || !strings.Contains(d, "[q] quit") {
		t.Errorf("unexpected dialog output: %s", d)
	}

	// openURL
	cmd := openURL("http://127.0.0.1:8000")
	if cmd == nil {
		t.Fatal("openURL returned nil cmd")
	}
	msgs := run(cmd)
	if len(msgs) == 0 {
		t.Fatal("openURL produced no msgs")
	}
	if status, ok := msgs[0].(StatusMsg); !ok || status.Text == "" {
		t.Errorf("openURL unexpected msg: %v", msgs[0])
	}

	// checkDB
	dbCmd := checkDB("", "mariadb")
	if dbCmd == nil {
		t.Fatal("checkDB returned nil")
	}
	dbMsgs := run(dbCmd)
	if len(dbMsgs) == 0 {
		t.Fatal("checkDB produced no msgs")
	}
	if check, ok := dbMsgs[0].(dbCheckMsg); !ok || check.engine != "mariadb" {
		t.Errorf("checkDB unexpected result: %v", dbMsgs[0])
	}

	// dbLine
	if l := dbLine(dbUp, "mariadb", false); !strings.Contains(l, "is running") {
		t.Errorf("dbLine dbUp failed: %s", l)
	}
	if l := dbLine(dbDown, "mariadb", true); !strings.Contains(l, "not running") || !strings.Contains(l, "Ctrl+S") {
		t.Errorf("dbLine dbDown failed: %s", l)
	}
	if l := dbLine(dbUnknown, "mariadb", false); !strings.Contains(l, "checking") {
		t.Errorf("dbLine dbUnknown failed: %s", l)
	}

	// dbRows
	rowsUp := dbRows(dbUp, "", "mariadb")
	if len(rowsUp) != 1 || !strings.Contains(rowsUp[0], "running") {
		t.Errorf("dbRows up unexpected: %v", rowsUp)
	}
	rowsDown := dbRows(dbDown, "socket missing", "mariadb")
	if len(rowsDown) < 2 {
		t.Errorf("dbRows down should have reason row: %v", rowsDown)
	}

	// startDBJob
	sup := core.NewSupervisor()
	startCmd := startDBJob(sup, "", "mariadb")
	if startCmd == nil {
		t.Fatal("startDBJob returned nil")
	}

	nilCmd := startDBJob(nil, "", "mariadb")
	if nilCmd == nil {
		t.Fatal("startDBJob with nil sup returned nil")
	}
}

func TestListNavAllKeys(t *testing.T) {
	var l listNav
	n := 20
	rows := 5

	for _, k := range []string{"up", "k", "down", "j", "pgup", "pgdown", "home", "g", "end", "G"} {
		if !l.key(k, n, rows) {
			t.Errorf("expected true for key %s", k)
		}
	}
	if l.key("invalid_key", n, rows) {
		t.Error("expected false for invalid_key")
	}
}

