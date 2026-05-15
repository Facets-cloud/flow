package dashboard

import "github.com/charmbracelet/lipgloss"

// Status glyphs. Kept here so the view layer and any external consumer
// (e.g. tests) share one source of truth.
const (
	GlyphWorking  = "●"
	GlyphAwaiting = "◐"
	GlyphStale    = "⚠"
	GlyphDone     = "✓"
	GlyphBacklog  = "○"
	GlyphPlaybook = "◆"
)

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true)

	dateStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	sectionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Bold(true)

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("237"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	// Status glyph colors mirror the human reading of each section:
	// working = active green, awaiting = amber, stale = red.
	workingGlyphStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("78")).
				Bold(true)
	awaitingGlyphStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)
	staleGlyphStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)
	doneGlyphStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))
	backlogGlyphStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244"))
	playbookGlyphStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("141")).
				Bold(true)

	priHighStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	priMedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	priLowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	slugStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("231"))
	projectStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
	contextStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))
	waitingContextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220"))

	hashStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("141"))

	footerKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("231")).
			Bold(true)
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	// Pane chrome. Title above each box; the box itself uses a rounded
	// border. Active pane gets a bright border + bright title so the
	// user can tell which pane ←→ will move them out of.
	paneTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Bold(true)
	paneActiveTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("39")).
				Bold(true)
	paneCountStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
	paneBorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("237"))
	paneActiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("39"))

	// Highlight applied to characters in a row's slug/project label
	// that matched the active search query. Bright yellow + bold so
	// hits stand out against both the muted project tint and the
	// bright slug tint.
	matchHighlightStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)
)

// priorityShort returns the 2-char tag rendered next to a row.
func priorityShort(p string) string {
	switch p {
	case "high":
		return "hi"
	case "medium":
		return "me"
	case "low":
		return "lo"
	default:
		return "  "
	}
}

// priorityStyleFor returns the lipgloss style for a priority value.
func priorityStyleFor(p string) lipgloss.Style {
	switch p {
	case "high":
		return priHighStyle
	case "medium":
		return priMedStyle
	default:
		return priLowStyle
	}
}
