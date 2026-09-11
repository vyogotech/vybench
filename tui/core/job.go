package core

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Step is one unit of work in a Job: an external command, or a Go function
// when Cmd is nil.
type Step struct {
	Label string
	Cmd   *exec.Cmd
	Fn    func(log func(string)) error
}

// JobSpec describes the steps of a Job.
type JobSpec struct {
	Steps []Step
	// OnFailure runs when a step fails or the job is cancelled, e.g. to remove
	// a half-built bench directory so the name can be reused.
	OnFailure func()
}

// ErrCancelled is the result of a job the user cancelled.
var ErrCancelled = errors.New("cancelled")

// maxLine bounds a single output line. Longer lines are split rather than
// failing the scanner, which would otherwise stop reading and block the child.
const maxLine = 64 * 1024

// Job runs its steps one after another in the background and streams their
// combined stdout and stderr, one sanitised line at a time.
type Job struct {
	lines    chan string
	quit     chan struct{}
	quitOnce sync.Once

	mu        sync.Mutex
	cur       *exec.Cmd
	err       error
	cancelled bool
}

// StartJob starts running spec in a new goroutine.
func StartJob(spec JobSpec) *Job {
	j := &Job{lines: make(chan string, 256), quit: make(chan struct{})}
	go j.run(spec)
	return j
}

// Lines yields output. It is closed once the job has finished, after which
// Err reports the outcome.
func (j *Job) Lines() <-chan string { return j.lines }

// Err is nil on success, ErrCancelled after Cancel, or the failing step's error.
func (j *Job) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// Cancel terminates the running command and every process it started, and
// skips the remaining steps.
func (j *Job) Cancel() {
	j.quitOnce.Do(func() { close(j.quit) })
	j.mu.Lock()
	j.cancelled = true
	cmd := j.cur
	j.mu.Unlock()
	if cmd != nil {
		terminate(cmd)
	}
}

func (j *Job) isCancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelled
}

func (j *Job) setCurrent(cmd *exec.Cmd) {
	j.mu.Lock()
	j.cur = cmd
	j.mu.Unlock()
}

func (j *Job) run(spec JobSpec) {
	defer close(j.lines)
	var err error
	for _, s := range spec.Steps {
		if j.isCancelled() {
			err = ErrCancelled
			break
		}
		if s.Label != "" {
			j.emit("▶ " + s.Label)
		}
		switch {
		case s.Cmd != nil:
			err = j.runCmd(s.Cmd)
		case s.Fn != nil:
			err = s.Fn(j.emit)
		}
		if j.isCancelled() {
			err = ErrCancelled
		}
		if err != nil {
			break
		}
	}
	if err != nil && spec.OnFailure != nil {
		spec.OnFailure()
	}
	j.mu.Lock()
	j.err = err
	j.mu.Unlock()
}

// emit sends a line to the reader, dropping it once the job is cancelled so a
// reader that stopped listening can never block the job goroutine.
func (j *Job) emit(line string) {
	select {
	case j.lines <- sanitize(line):
	case <-j.quit:
	}
}

func (j *Job) runCmd(cmd *exec.Cmd) error {
	pr, pw := io.Pipe()
	cmd.Stdin = nil
	cmd.Stdout = pw
	cmd.Stderr = pw
	// A grandchild that inherits the pipe (a daemon bench spawns, say) must
	// not keep Wait blocked after the command itself has exited.
	cmd.WaitDelay = 2 * time.Second
	detach(cmd)

	j.emit("$ " + DisplayCommand(cmd))
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return err
	}
	j.setCurrent(cmd)
	defer j.setCurrent(nil)

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		_ = pw.Close()
	}()

	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 4096), 2*maxLine)
	sc.Split(scanLines)
	for sc.Scan() {
		j.emit(sc.Text())
	}
	if sc.Err() != nil {
		_, _ = io.Copy(io.Discard, pr)
	}

	err := <-waitErr
	if j.isCancelled() {
		return ErrCancelled
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s exited with status %d", filepath.Base(cmd.Path), exitErr.ExitCode())
	}
	return err
}

// scanLines splits on \n, \r\n and bare \r (progress output), and hands over
// any line longer than maxLine in pieces.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\r' {
			if i+1 == len(data) && !atEOF {
				return 0, nil, nil // need one more byte to see whether \n follows
			}
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
		}
		return i + 1, data[:i], nil
	}
	if atEOF || len(data) >= maxLine {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// sanitize strips escape sequences and control characters, which would
// otherwise move the cursor and corrupt the TUI layout.
func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// DisplayCommand renders a command for the output log with password values
// masked.
func DisplayCommand(cmd *exec.Cmd) string {
	args := append([]string(nil), cmd.Args...)
	if len(args) > 0 {
		args[0] = filepath.Base(args[0])
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || !strings.Contains(strings.ToLower(a), "password") {
			continue
		}
		if k, _, ok := strings.Cut(a, "="); ok {
			args[i] = k + "=****"
		} else if i+1 < len(args) {
			args[i+1] = "****"
			i++
		}
	}
	return strings.Join(args, " ")
}
