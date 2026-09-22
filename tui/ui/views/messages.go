package views

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/vyogotech/vybench/tui/core"
)

// Messages the views exchange through the root model. Anything that is not a
// key or mouse event is broadcast to every view, so a view on another tab
// still sees, say, a catalog that finished loading.

// BenchSwitchedMsg is broadcast after the active bench changes.
type BenchSwitchedMsg struct{ Bench core.ActiveBench }

// BenchesChangedMsg is broadcast after a bench is created, attached or dropped.
type BenchesChangedMsg struct{}

// SitesChangedMsg is broadcast after a site is created or dropped.
type SitesChangedMsg struct{}

// SwitchToMarketplaceMsg requests switching to the Marketplace tab with a target site preselected.
type SwitchToMarketplaceMsg struct {
	TargetSite string
}

// AppsChangedMsg is broadcast after an app is installed into the bench.
type AppsChangedMsg struct{}

// ServicesChangedMsg is broadcast after services were started or stopped.
type ServicesChangedMsg struct{}

// StatusMsg shows a line in the status bar.
type StatusMsg struct {
	Text string
	Err  bool
}

// RunJobMsg asks the root model to run a job in the output overlay. After is
// broadcast when the job succeeds and Failed when it fails; Success, or the
// error followed by FailHint, is shown in the status bar.
type RunJobMsg struct {
	Title    string
	Spec     core.JobSpec
	Success  string
	After    []tea.Msg
	Failed   []tea.Msg
	FailHint string
}

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

func setStatus(text string) tea.Cmd { return emit(StatusMsg{Text: text}) }

func setError(err error) tea.Cmd { return emit(StatusMsg{Text: err.Error(), Err: true}) }

func runJob(title string, spec core.JobSpec, success string, after ...tea.Msg) tea.Cmd {
	return emit(RunJobMsg{Title: title, Spec: spec, Success: success, After: after})
}
