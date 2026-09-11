// Package theme — Vyogo design tokens for the vybench TUI.
package theme

import "github.com/charmbracelet/lipgloss"

// ──────────────────────────────────────────────────────────────────────────────
// Colour palette
// ──────────────────────────────────────────────────────────────────────────────

var (
	ColorPrimary    = lipgloss.Color("#00875A") // Vyogo Teal
	ColorPrimaryDim = lipgloss.Color("#005C3D")
	ColorAccent     = lipgloss.Color("#00BFAE") // Bright teal for highlights
	ColorBgBase     = lipgloss.Color("#141917") // Slate near-black
	ColorBgSurface  = lipgloss.Color("#1E2327") // Surface card
	ColorBgMuted    = lipgloss.Color("#252D2A") // Subtle row bg
	ColorText       = lipgloss.Color("#E8EDE9") // Primary text
	ColorMuted      = lipgloss.Color("#6B7C74") // Subdued text
	ColorBorder     = lipgloss.Color("#2C3830") // Panel borders
	ColorSuccess    = lipgloss.Color("#2DBF70") // Green
	ColorWarn       = lipgloss.Color("#F59E0B") // Amber
	ColorError      = lipgloss.Color("#EF4444") // Red
	ColorInfo       = lipgloss.Color("#38BDF8") // Cyan

	// DB engine badge colours
	ColorMariaDB  = lipgloss.Color("#06B6D4") // Cyan
	ColorPostgres = lipgloss.Color("#818CF8") // Indigo
)

// ──────────────────────────────────────────────────────────────────────────────
// Shared styles
// ──────────────────────────────────────────────────────────────────────────────

var (
	StyleBase = lipgloss.NewStyle().
			Background(ColorBgBase).
			Foreground(ColorText)

	StyleBold = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorText)

	StyleMuted = lipgloss.NewStyle().
			Foreground(ColorMuted)

	StylePrimary = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	StyleAccent = lipgloss.NewStyle().
			Foreground(ColorAccent)

	// Tab styles
	StyleTabActive = lipgloss.NewStyle().
			Foreground(ColorBgBase).
			Background(ColorPrimary).
			Bold(true).
			Padding(0, 1)

	StyleTabInactive = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Background(ColorBgSurface).
				Padding(0, 1)

	// Panel / card
	StylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Background(ColorBgSurface)

	// Table row styles
	StyleRowNormal = lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorBgBase)

	StyleRowSelected = lipgloss.NewStyle().
				Foreground(ColorBgBase).
				Background(ColorPrimary).
				Bold(true)

	StyleRowAlt = lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorBgMuted)

	// Status badges
	StyleBadgeRunning = lipgloss.NewStyle().
				Foreground(ColorSuccess).
				Bold(true)

	StyleBadgeStopped = lipgloss.NewStyle().
				Foreground(ColorMuted)

	StyleBadgeWarn = lipgloss.NewStyle().
			Foreground(ColorWarn).
			Bold(true)

	StyleBadgeMariaDB = lipgloss.NewStyle().
				Foreground(ColorMariaDB).
				Bold(true)

	StyleBadgePostgres = lipgloss.NewStyle().
				Foreground(ColorPostgres).
				Bold(true)

	StyleBadgeActive = lipgloss.NewStyle().
				Foreground(ColorSuccess).
				Bold(true)

	// Help bar at the bottom
	StyleHelp = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Background(ColorBgSurface).
			Padding(0, 1)

	StyleHelpKey = lipgloss.NewStyle().
			Foreground(ColorAccent).
			Bold(true)

	// Header bar
	StyleHeader = lipgloss.NewStyle().
			Background(ColorBgSurface).
			Foreground(ColorText).
			Bold(true).
			Padding(0, 1)

	StyleHeaderTitle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	// Modal overlay
	StyleModal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Background(ColorBgSurface).
			Padding(1, 2)

	// Form dialog: like StyleModal without the vertical padding, so a form
	// fits on a 24-row terminal.
	StyleDialog = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Background(ColorBgSurface).
			Padding(0, 2)

	// Input field
	StyleInput = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(ColorPrimary).
			Padding(0, 1)

	// Log level colours
	StyleLogInfo  = lipgloss.NewStyle().Foreground(ColorInfo)
	StyleLogWarn  = lipgloss.NewStyle().Foreground(ColorWarn)
	StyleLogError = lipgloss.NewStyle().Foreground(ColorError)
	StyleLogOK    = lipgloss.NewStyle().Foreground(ColorSuccess)
)

// DBBadge returns a styled badge for a database engine string.
func DBBadge(engine string) string {
	switch engine {
	case "postgres":
		return StyleBadgePostgres.Render("[Postgres]")
	default:
		return StyleBadgeMariaDB.Render("[MariaDB]")
	}
}

// ActiveBadge returns a styled active/inactive indicator.
func ActiveBadge(active bool) string {
	if active {
		return StyleBadgeActive.Render("★ active")
	}
	return StyleMuted.Render("  ──")
}
