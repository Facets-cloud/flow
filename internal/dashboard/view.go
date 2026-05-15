package dashboard

import (
	"database/sql"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// viewMode is the top-level layout selector. `v` toggles between
// viewByStatus (the default — working/awaiting/stale/backlog/playbooks
// panes) and viewByProject (projects on the left, the selected
// project's tasks on the right).
type viewMode int

const (
	viewByStatus viewMode = iota
	viewByProject
)

// paneKind identifies one of the dashboard's sub-panes. Order matches
// the visual top-to-bottom layout and the cycle order for ←/→.
type paneKind int

const (
	paneWorking paneKind = iota
	paneAwaiting
	paneStale
	paneBacklog
	panePlaybooks
	paneActivity
	numPanes
)

var paneTitles = [numPanes]string{
	"working", "awaiting", "stale  (untouched 7d+)", "backlog", "playbooks", "recent activity",
}

// paneState carries the per-pane navigation state. cursor is the row
// index inside the pane's row list (used in task panes; ignored on
// the activity pane). scrollOffset is the first visible row within
// the pane's content viewport.
type paneState struct {
	cursor       int
	scrollOffset int
}

// Model is the bubbletea model for the dashboard. Each section
// (working / awaiting / stale / backlog / recent activity) is rendered
// as a bordered pane with its own scroll state; ←/→ moves focus
// between non-empty panes. A search bar (toggled with `/`) and facet
// hotkeys (`p`/`a`/`t`) narrow the visible rows.
type Model struct {
	loader    *Loader
	flowBin   string
	rawSnap   *Snapshot         // unfiltered snapshot from the loader
	snap      *Snapshot         // rawSnap with the active filter applied
	err       error
	width     int
	height    int
	active    paneKind
	panes     [numPanes]paneState
	status    string
	filter    filter
	searching bool              // search bar focused; keystrokes go to the input
	search    textinput.Model

	// Playbooks sub-focus: when active=panePlaybooks and runsFocused
	// is true, ↑↓ navigates the runs sub-pane (right column), enter
	// opens the selected run, esc returns to the playbook list.
	runsFocused bool
	runCursor   int

	// Project view: `v` toggles between status and project layouts.
	// projectCursor is the row index into uniqueProjects(rawSnap)
	// when view == viewByProject; projectTaskFocus moves focus to
	// the right-side task pane (where ↑↓ then navigates the flattened
	// task list and enter opens the selected task).
	view              viewMode
	projectCursor     int
	projectTaskFocus  bool
	projectTaskCursor int
}

func NewModel(loader *Loader, flowBin string) *Model {
	m := &Model{loader: loader, flowBin: flowBin}
	ti := textinput.New()
	ti.Placeholder = "search slug, project, name, tag, assignee…"
	ti.Prompt = ""
	ti.CharLimit = 80
	ti.Width = 60
	m.search = ti
	m.reload()
	m.active = m.firstNonEmptyPane()
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

// reload re-queries the DB and re-bucketizes the snapshot, then
// reapplies any active filter onto the fresh data.
func (m *Model) reload() {
	snap, err := m.loader.Snapshot()
	m.rawSnap = snap
	m.err = err
	m.applyFilter()
}

// applyFilter recomputes m.snap from m.rawSnap and m.filter, then
// clamps per-pane cursors against the new row counts and advances
// the active pane if it emptied out under the filter.
func (m *Model) applyFilter() {
	if m.rawSnap == nil {
		return
	}
	m.snap = filterSnapshot(m.rawSnap, m.filter)
	for p := paneKind(0); p < numPanes; p++ {
		n := m.paneRowCount(p)
		if n == 0 {
			m.panes[p].cursor = 0
			m.panes[p].scrollOffset = 0
			continue
		}
		if m.panes[p].cursor >= n {
			m.panes[p].cursor = n - 1
		}
		if m.panes[p].cursor < 0 {
			m.panes[p].cursor = 0
		}
	}
	if m.paneRowCount(m.active) == 0 {
		m.active = m.firstNonEmptyPane()
		m.runsFocused = false
	}
	// Clamp runs sub-focus against the currently selected playbook.
	if m.runsFocused {
		pb := m.selectedPlaybook()
		if pb == nil || len(pb.Runs) == 0 {
			m.runsFocused = false
			m.runCursor = 0
		} else if m.runCursor >= len(pb.Runs) {
			m.runCursor = len(pb.Runs) - 1
		}
	}
	// Clamp project-view cursors against the current filtered data.
	projs := uniqueProjects(m.snap)
	if m.projectCursor >= len(projs) {
		m.projectCursor = len(projs) - 1
	}
	if m.projectCursor < 0 {
		m.projectCursor = 0
	}
	if m.projectTaskFocus {
		rows := m.projectTaskList(m.currentProjectSlug())
		if len(rows) == 0 {
			m.projectTaskFocus = false
			m.projectTaskCursor = 0
		} else if m.projectTaskCursor >= len(rows) {
			m.projectTaskCursor = len(rows) - 1
		}
	}
}

func (m *Model) paneRowCount(p paneKind) int {
	if m.snap == nil {
		return 0
	}
	switch p {
	case paneWorking:
		return len(m.snap.Working)
	case paneAwaiting:
		return len(m.snap.Awaiting)
	case paneStale:
		return len(m.snap.Stale)
	case paneBacklog:
		return len(m.snap.Backlog)
	case panePlaybooks:
		return len(m.snap.Playbooks)
	case paneActivity:
		return len(m.snap.Activity)
	}
	return 0
}

func (m *Model) paneTaskRows(p paneKind) []TaskRow {
	if m.snap == nil {
		return nil
	}
	switch p {
	case paneWorking:
		return m.snap.Working
	case paneAwaiting:
		return m.snap.Awaiting
	case paneStale:
		return m.snap.Stale
	case paneBacklog:
		return m.snap.Backlog
	}
	return nil
}

func (m *Model) firstNonEmptyPane() paneKind {
	for p := paneKind(0); p < numPanes; p++ {
		if m.paneRowCount(p) > 0 {
			return p
		}
	}
	return paneWorking
}

// nextNonEmptyPane returns the next non-empty pane in cycle direction
// (+1 = forward, -1 = backward). Returns the current pane when nothing
// else is populated.
func (m *Model) nextNonEmptyPane(dir int) paneKind {
	for step := 1; step < int(numPanes); step++ {
		next := paneKind((int(m.active) + dir*step + int(numPanes)) % int(numPanes))
		if m.paneRowCount(next) > 0 {
			return next
		}
	}
	return m.active
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// When the search bar is focused, keystrokes flow to the input
		// (except for the two that exit search mode).
		if m.searching {
			switch msg.String() {
			case "esc":
				m.search.Blur()
				m.search.SetValue("")
				m.filter.query = ""
				m.searching = false
				m.applyFilter()
				return m, nil
			case "enter":
				m.search.Blur()
				m.searching = false
				return m, nil
			}
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			if v := m.search.Value(); v != m.filter.query {
				m.filter.query = v
				m.applyFilter()
			}
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			// Layered escape:
			//   1. Exit a sub-focus first (runs sub-pane or project
			//      task pane).
			//   2. Otherwise clear any active filter.
			//   3. Otherwise no-op (q quits).
			if m.view == viewByProject && m.projectTaskFocus {
				m.projectTaskFocus = false
				return m, nil
			}
			if m.runsFocused {
				m.runsFocused = false
				return m, nil
			}
			if !m.filter.empty() {
				m.filter = filter{}
				m.search.SetValue("")
				m.applyFilter()
			}
			return m, nil
		case "/":
			m.searching = true
			m.search.SetValue(m.filter.query)
			m.search.Focus()
			return m, textinput.Blink
		case "p":
			m.filter.priority = cycleString(m.filter.priority, []string{"high", "medium", "low"})
			m.applyFilter()
			return m, nil
		case "a":
			m.filter.assignee = cycleString(m.filter.assignee, uniqueAssignees(m.rawSnap))
			m.applyFilter()
			return m, nil
		case "t":
			m.filter.tag = cycleString(m.filter.tag, uniqueTags(m.rawSnap))
			m.applyFilter()
			return m, nil
		case "P":
			m.filter.project = cycleString(m.filter.project, uniqueProjects(m.rawSnap))
			m.applyFilter()
			return m, nil
		case "v":
			if m.view == viewByStatus {
				m.view = viewByProject
				if m.projectCursor < 0 {
					m.projectCursor = 0
				}
			} else {
				m.view = viewByStatus
				m.projectTaskFocus = false
			}
			return m, nil
		case "right", "l":
			// Context-sensitive drills:
			//   - project view: focus the right task pane.
			//   - playbooks pane: focus the runs sub-pane.
			//   - otherwise: cycle to the next pane.
			if m.view == viewByProject && !m.projectTaskFocus {
				if rows := m.projectTaskList(m.currentProjectSlug()); len(rows) > 0 {
					m.projectTaskFocus = true
					m.projectTaskCursor = 0
					return m, nil
				}
			}
			if m.active == panePlaybooks && !m.runsFocused {
				if pb := m.selectedPlaybook(); pb != nil && len(pb.Runs) > 0 {
					m.runsFocused = true
					if m.runCursor < 0 || m.runCursor >= len(pb.Runs) {
						m.runCursor = 0
					}
					return m, nil
				}
			}
			m.runsFocused = false
			m.active = m.nextNonEmptyPane(+1)
			return m, nil
		case "tab":
			m.runsFocused = false
			m.active = m.nextNonEmptyPane(+1)
			return m, nil
		case "left", "h":
			// Context-sensitive: leaving a sub-pane returns focus
			// instead of cycling away.
			if m.view == viewByProject && m.projectTaskFocus {
				m.projectTaskFocus = false
				return m, nil
			}
			if m.runsFocused {
				m.runsFocused = false
				return m, nil
			}
			m.active = m.nextNonEmptyPane(-1)
			return m, nil
		case "shift+tab":
			m.runsFocused = false
			m.active = m.nextNonEmptyPane(-1)
			return m, nil
		case "up", "k":
			if m.view == viewByProject {
				if m.projectTaskFocus {
					if m.projectTaskCursor > 0 {
						m.projectTaskCursor--
					}
				} else if m.projectCursor > 0 {
					m.projectCursor--
					m.projectTaskCursor = 0
				}
				return m, nil
			}
			if m.runsFocused {
				if m.runCursor > 0 {
					m.runCursor--
				}
				return m, nil
			}
			m.moveInActive(-1)
			return m, nil
		case "down", "j":
			if m.view == viewByProject {
				if m.projectTaskFocus {
					rows := m.projectTaskList(m.currentProjectSlug())
					if m.projectTaskCursor < len(rows)-1 {
						m.projectTaskCursor++
					}
					return m, nil
				}
				projs := uniqueProjects(m.rawSnap)
				if m.projectCursor < len(projs)-1 {
					m.projectCursor++
					m.projectTaskCursor = 0
				}
				return m, nil
			}
			if m.runsFocused {
				if pb := m.selectedPlaybook(); pb != nil && m.runCursor < len(pb.Runs)-1 {
					m.runCursor++
				}
				return m, nil
			}
			m.moveInActive(+1)
			return m, nil
		case "pgup", "ctrl+u":
			m.scrollActive(-m.paneContentHeight(m.active) / 2)
			return m, nil
		case "pgdown", "ctrl+d":
			m.scrollActive(+m.paneContentHeight(m.active) / 2)
			return m, nil
		case "home", "g":
			m.panes[m.active].cursor = 0
			m.panes[m.active].scrollOffset = 0
			return m, nil
		case "end", "G":
			n := m.paneRowCount(m.active)
			if n > 0 {
				m.panes[m.active].cursor = n - 1
			}
			m.ensureActiveCursorVisible()
			return m, nil
		case "r":
			m.reload()
			m.status = "reloaded"
			return m, nil
		case "enter":
			// Project view, task pane focused: open the selected task.
			if m.view == viewByProject && m.projectTaskFocus {
				rows := m.projectTaskList(m.currentProjectSlug())
				if m.projectTaskCursor < 0 || m.projectTaskCursor >= len(rows) {
					return m, nil
				}
				return m, m.openTaskCmd(rows[m.projectTaskCursor].Task.Slug)
			}
			// Playbooks pane:
			//   - on the playbook list: enter spawns a fresh run
			//     (`flow run playbook <slug>`).
			//   - on the runs sub-pane: enter opens the selected
			//     existing run (`flow do <run-slug>`).
			if m.active == panePlaybooks {
				pb := m.selectedPlaybook()
				if pb == nil {
					return m, nil
				}
				if !m.runsFocused {
					return m, m.runPlaybookCmd(pb.Playbook.Slug)
				}
				if m.runCursor < 0 || m.runCursor >= len(pb.Runs) {
					return m, nil
				}
				return m, m.openTaskCmd(pb.Runs[m.runCursor].Task.Slug)
			}
			slug := m.selectedTaskSlug()
			if slug == "" {
				return m, nil
			}
			return m, m.openTaskCmd(slug)
		}

	case execFinishedMsg:
		m.reload()
		switch {
		case msg.err != nil && msg.kind == "playbook":
			m.status = fmt.Sprintf("run playbook %s failed: %v", msg.slug, msg.err)
		case msg.err != nil:
			m.status = fmt.Sprintf("flow do %s: %v", msg.slug, msg.err)
		case msg.kind == "playbook":
			m.status = fmt.Sprintf("started new run of %s", msg.slug)
		default:
			m.status = fmt.Sprintf("opened %s", msg.slug)
		}
		return m, nil
	}
	return m, nil
}

// moveInActive moves within the active pane. In task panes that means
// moving the cursor (with auto-scroll). In the activity pane it just
// scrolls.
func (m *Model) moveInActive(dir int) {
	if m.active == paneActivity {
		m.scrollActive(dir)
		return
	}
	n := m.paneRowCount(m.active)
	if n == 0 {
		return
	}
	c := m.panes[m.active].cursor + dir
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	m.panes[m.active].cursor = c
	m.ensureActiveCursorVisible()
}

// scrollActive adjusts only the active pane's scrollOffset, clamping
// into the legal range. Cursor is not touched (used by PgUp/PgDn and
// by the activity pane's ↑/↓ handlers).
func (m *Model) scrollActive(delta int) {
	off := m.panes[m.active].scrollOffset + delta
	off = clamp(off, 0, m.paneMaxScrollOffset(m.active))
	m.panes[m.active].scrollOffset = off
}

// ensureActiveCursorVisible nudges the active pane's scrollOffset so
// the cursor row's first rendered line is inside the viewport.
// Continuation lines of a wrapped row may still spill off the bottom;
// the user can PgDn / arrow further to bring them into view.
func (m *Model) ensureActiveCursorVisible() {
	if m.active == paneActivity {
		return
	}
	first := m.cursorRowFirstLine(m.active)
	off := m.panes[m.active].scrollOffset
	h := m.paneContentHeight(m.active)
	if first < off {
		off = first
	} else if first >= off+h {
		off = first - h + 1
	}
	off = clamp(off, 0, m.paneMaxScrollOffset(m.active))
	m.panes[m.active].scrollOffset = off
}

// paneMaxScrollOffset uses rendered-line count, not row count, so
// wrapped rows scroll consistently.
func (m *Model) paneMaxScrollOffset(p paneKind) int {
	n := m.paneTotalLines(p)
	h := m.paneContentHeight(p)
	if n <= h {
		return 0
	}
	return n - h
}

// paneTotalLines is how many rendered lines pane p produces. With
// labels truncated rather than wrapped, every row is exactly one
// line — so this equals the row count for every pane.
func (m *Model) paneTotalLines(p paneKind) int {
	return m.paneRowCount(p)
}

// cursorRowFirstLine returns the rendered-line index of the cursor
// row in pane p. Since rows truncate (don't wrap), the cursor's row
// index equals its line index.
func (m *Model) cursorRowFirstLine(p paneKind) int {
	n := m.paneRowCount(p)
	if n == 0 {
		return 0
	}
	c := m.panes[p].cursor
	if c < 0 {
		return 0
	}
	if c >= n {
		return n - 1
	}
	return c
}

// selectedTaskSlug returns the slug of the row the cursor is on in the
// active task pane, or "" when the active pane has no enterable row.
func (m *Model) selectedTaskSlug() string {
	rows := m.paneTaskRows(m.active)
	if len(rows) == 0 {
		return ""
	}
	c := m.panes[m.active].cursor
	if c < 0 || c >= len(rows) {
		return ""
	}
	return rows[c].Task.Slug
}

type execFinishedMsg struct {
	slug string
	kind string // "task" — flow do; "playbook" — flow run playbook
	err  error
}

// openTaskCmd suspends the TUI and runs `flow do <slug>`. flow do
// spawns or focuses a terminal tab on its own and exits quickly, so
// the dashboard resumes within a second.
func (m *Model) openTaskCmd(slug string) tea.Cmd {
	cmd := exec.Command(m.flowBin, "do", slug)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execFinishedMsg{slug: slug, kind: "task", err: err}
	})
}

// runPlaybookCmd suspends the TUI and runs `flow run playbook <slug>`,
// which creates a new playbook-run task and spawns its tab. The
// dashboard reloads on return so the new run appears at the top of
// the playbook's runs sub-pane.
func (m *Model) runPlaybookCmd(slug string) tea.Cmd {
	cmd := exec.Command(m.flowBin, "run", "playbook", slug)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execFinishedMsg{slug: slug, kind: "playbook", err: err}
	})
}

// ----- rendering -----

func (m *Model) View() string {
	if m.snap == nil && m.err != nil {
		return errorStyle.Render("error: "+m.err.Error()) + "\n"
	}
	if m.view == viewByProject {
		return m.viewProjectMode()
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString("  " + m.renderCounts())
	b.WriteString("\n")
	if m.showSearchLine() {
		b.WriteString("  " + m.renderSearchLine())
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if m.err != nil {
		b.WriteString("  " + errorStyle.Render("error: "+m.err.Error()) + "\n")
	}

	visiblePanes := m.visiblePanes()
	if len(visiblePanes) == 0 {
		b.WriteString("\n  " + mutedStyle.Render("No tasks yet — your dashboard is empty.") + "\n")
	} else {
		leftW, rightW := m.columnWidths()
		leftCol := m.renderLeftColumn(visiblePanes, leftW)
		var combined string
		if rightW > 0 {
			var rightCol string
			if m.active == panePlaybooks {
				rightCol = m.renderPlaybookRightColumn(rightW)
			} else {
				rightCol = m.renderDetailPane(rightW)
			}
			combined = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)
		} else {
			combined = leftCol
		}
		// Apply the 2-column left margin to every line of the column
		// strip, not just the first. Single-prefix would leave pane
		// borders flush against column 0.
		b.WriteString(indentBlock(combined, 2))
	}

	b.WriteString("\n")
	if m.status != "" {
		b.WriteString("  " + mutedStyle.Render(m.status) + "\n")
	}
	b.WriteString(m.footer())
	return b.String()
}

// columnWidths splits the horizontal space between the left list of
// section panes and the right-side detail pane. Width is adaptive:
// left takes only as much as the widest task row actually needs (so
// short slugs leave more room for the detail pane), bounded by a
// 65%-of-total cap so detail always gets a fair share. Returns
// (leftW, 0) when the terminal is too narrow to host both columns.
func (m *Model) columnWidths() (left, right int) {
	const (
		outerMargin = 4 // 2 left + 2 right
		gap         = 2 // between the two columns
		minSplit    = 100
		minLeft     = 50
		minRight    = 38
	)
	total := m.width - outerMargin
	if total < minSplit {
		return total, 0
	}

	natural := m.naturalLeftWidth()
	maxLeft := total * 70 / 100 // cap left at 70% — detail still gets ≥ 30%
	left = natural
	if left < minLeft {
		left = minLeft
	}
	if left > maxLeft {
		left = maxLeft
	}
	right = total - left - gap

	// If detail would be too narrow, steal back from left.
	if right < minRight {
		right = minRight
		left = total - right - gap
	}
	if left < minLeft {
		return total, 0
	}
	return left, right
}

// naturalLeftWidth returns the ideal outer width for the left column,
// driven off the raw (unfiltered) data so the layout doesn't shift
// while the user types a search. Chrome is computed precisely from
// the actual max assignee + reltime widths so the right-side columns
// (priority/assignee/reltime) never get clipped — only the label
// truncates with an ellipsis when the row exceeds budget.
func (m *Model) naturalLeftWidth() int {
	if m.rawSnap == nil {
		return 80
	}
	const paneBorder = 2
	maxLabel := 0
	maxAssignee := 0
	maxRelTime := 0
	for _, rows := range [][]TaskRow{
		m.rawSnap.Working,
		m.rawSnap.Awaiting,
		m.rawSnap.Stale,
		m.rawSnap.Backlog,
	} {
		for _, r := range rows {
			if n := plainLabelLen(r); n > maxLabel {
				maxLabel = n
			}
			if r.Task.Assignee.Valid {
				if n := len(r.Task.Assignee.String); n > maxAssignee {
					maxAssignee = n
				}
			}
			if n := len(r.RelTime); n > maxRelTime {
				maxRelTime = n
			}
		}
	}
	if maxLabel == 0 {
		return 60
	}
	// chrome = pane border + row chrome + label-pri gap + priority + pri-reltime gap + reltime.
	chrome := paneBorder + rowLeftChrome + rowMidGap + rowPriWidth + rowRightGap + maxRelTime
	if maxAssignee > 0 {
		// `@<name>` plus the gap before the priority column.
		chrome += maxAssignee + 1 + rowRightGap
	}
	return maxLabel + chrome
}

func (m *Model) renderLeftColumn(visible []paneKind, width int) string {
	var s strings.Builder
	for i, p := range visible {
		if i > 0 {
			s.WriteString("\n")
		}
		s.WriteString(m.renderPaneWithWidth(p, width))
	}
	return s.String()
}

// viewProjectMode renders the "by project" layout: a list of projects
// on the left, the selected project's tasks grouped by status on the
// right. Header/counts/filter/footer are reused from status mode so
// switching with `v` feels like a layout swap, not a different screen.
func (m *Model) viewProjectMode() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString("  " + m.renderCounts())
	b.WriteString("\n")
	if m.showSearchLine() {
		b.WriteString("  " + m.renderSearchLine())
		b.WriteString("\n")
	}
	b.WriteString("\n")

	projs := uniqueProjects(m.rawSnap)
	if len(projs) == 0 {
		b.WriteString("\n  " + mutedStyle.Render("No projects yet.") + "\n")
	} else {
		if m.projectCursor >= len(projs) {
			m.projectCursor = len(projs) - 1
		}
		if m.projectCursor < 0 {
			m.projectCursor = 0
		}
		leftW, rightW := m.columnWidths()
		leftCol := m.renderProjectsList(projs, leftW)
		var combined string
		if rightW > 0 {
			rightCol := m.renderProjectDetail(projs[m.projectCursor], rightW)
			combined = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)
		} else {
			combined = leftCol
		}
		b.WriteString(indentBlock(combined, 2))
	}

	b.WriteString("\n")
	if m.status != "" {
		b.WriteString("  " + mutedStyle.Render(m.status) + "\n")
	}
	b.WriteString(m.footer())
	return b.String()
}

// renderProjectsList renders the left-column list of projects, one
// row per project, with task counts and a cursor highlight.
func (m *Model) renderProjectsList(projs []string, outerWidth int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	contentH := m.projectViewContentHeight()

	// Pre-compute label width for alignment.
	maxLabel := 0
	for _, p := range projs {
		if len(p) > maxLabel {
			maxLabel = len(p)
		}
	}

	lines := make([]string, 0, len(projs))
	for i, p := range projs {
		cursor := "  "
		if i == m.projectCursor {
			cursor = cursorStyle.Render("›") + " "
		}
		counts := m.projectTaskCounts(p)
		summary := mutedStyle.Render(fmt.Sprintf("%d tasks", counts.total))
		lines = append(lines, fmt.Sprintf("%s%s   %s",
			cursor,
			padToVisible(slugStyle.Render(p), maxLabel),
			summary,
		))
	}
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padOrTrim(l, innerWidth)
	}

	titleStyle := paneActiveTitleStyle
	borderStyle := paneActiveBorderStyle
	if m.projectTaskFocus {
		// Right pane has focus — dim the project list.
		titleStyle = paneTitleStyle
		borderStyle = paneBorderStyle
	}
	title := titleStyle.Render("projects") +
		paneCountStyle.Render(fmt.Sprintf("  (%d)", len(projs)))
	box := borderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
	return title + "\n" + box
}

// projectViewContentHeight is the inner height shared by the
// projects pane (left) and the project-detail pane (right) in
// viewByProject mode. Mirrors paneContentHeight's reserves so the
// chrome math stays consistent.
func (m *Model) projectViewContentHeight() int {
	const (
		bottomReserve = 3
		paneChrome    = 3
	)
	topReserve := 4
	if m.showSearchLine() {
		topReserve = 5
	}
	h := m.height
	if h <= 0 {
		h = 24
	}
	inner := h - topReserve - bottomReserve - paneChrome
	if inner < 5 {
		inner = 5
	}
	return inner
}

type projectCounts struct {
	working, awaiting, stale, backlog, total int
}

func (m *Model) projectTaskCounts(slug string) projectCounts {
	if m.snap == nil {
		return projectCounts{}
	}
	var c projectCounts
	count := func(rows []TaskRow, into *int) {
		for _, r := range rows {
			if r.ProjectSlug == slug {
				*into++
				c.total++
			}
		}
	}
	count(m.snap.Working, &c.working)
	count(m.snap.Awaiting, &c.awaiting)
	count(m.snap.Stale, &c.stale)
	count(m.snap.Backlog, &c.backlog)
	return c
}

// renderProjectDetail renders the right pane in project view: the
// selected project's tasks grouped by status (working / awaiting /
// stale / backlog), inside one bordered box per status section
// when the section is non-empty.
func (m *Model) renderProjectDetail(slug string, outerWidth int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	contentH := m.projectViewContentHeight()

	// Collect tasks for this project from each status bucket.
	working := projectRowsFor(m.snap.Working, slug)
	awaiting := projectRowsFor(m.snap.Awaiting, slug)
	stale := projectRowsFor(m.snap.Stale, slug)
	backlog := projectRowsFor(m.snap.Backlog, slug)

	// Build the body as sectioned lines. Use single shared column
	// widths across all of the project's task rows so the columns
	// stay aligned across status sections.
	all := append([]TaskRow{}, working...)
	all = append(all, awaiting...)
	all = append(all, stale...)
	all = append(all, backlog...)
	cw := computeColWidths(all, innerWidth)

	// Track which row index in the flattened list each rendered line
	// corresponds to — so the cursor highlight lands on the right line.
	cursorIdx := -1
	if m.projectTaskFocus {
		cursorIdx = m.projectTaskCursor
	}
	var lines []string
	flatIdx := 0
	pushSection := func(name, glyph string, gs lipglossStyle, rows []TaskRow) {
		if len(rows) == 0 {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sectionStyle.Render(name)+
			paneCountStyle.Render(fmt.Sprintf("  (%d)", len(rows))))
		for _, r := range rows {
			isCursor := flatIdx == cursorIdx
			line := m.renderProjectTaskLine(r, gs, glyph, cw, isCursor)
			lines = append(lines, line)
			flatIdx++
		}
	}
	pushSection("working", GlyphWorking, workingGlyphStyle, working)
	pushSection("awaiting", GlyphAwaiting, awaitingGlyphStyle, awaiting)
	pushSection("stale", GlyphStale, staleGlyphStyle, stale)
	pushSection("backlog", GlyphBacklog, backlogGlyphStyle, backlog)

	if len(lines) == 0 {
		lines = []string{mutedStyle.Render("No tasks in this project under the current filter.")}
	}
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padOrTrim(l, innerWidth)
	}

	titleStyle := paneTitleStyle
	borderStyle := paneBorderStyle
	if m.projectTaskFocus {
		titleStyle = paneActiveTitleStyle
		borderStyle = paneActiveBorderStyle
	}
	title := titleStyle.Render(slug)
	box := borderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
	return title + "\n" + box
}

// currentProjectSlug returns the project slug under the project-list
// cursor in viewByProject. "" when there are no projects.
func (m *Model) currentProjectSlug() string {
	projs := uniqueProjects(m.rawSnap)
	if len(projs) == 0 {
		return ""
	}
	c := m.projectCursor
	if c < 0 {
		c = 0
	}
	if c >= len(projs) {
		c = len(projs) - 1
	}
	return projs[c]
}

// projectTaskList returns the selected project's tasks in display
// order (working, awaiting, stale, backlog) for cursor navigation
// inside the project task pane.
func (m *Model) projectTaskList(slug string) []TaskRow {
	if slug == "" || m.snap == nil {
		return nil
	}
	var out []TaskRow
	out = append(out, projectRowsFor(m.snap.Working, slug)...)
	out = append(out, projectRowsFor(m.snap.Awaiting, slug)...)
	out = append(out, projectRowsFor(m.snap.Stale, slug)...)
	out = append(out, projectRowsFor(m.snap.Backlog, slug)...)
	return out
}

func projectRowsFor(rows []TaskRow, slug string) []TaskRow {
	out := make([]TaskRow, 0)
	for _, r := range rows {
		if r.ProjectSlug == slug {
			out = append(out, r)
		}
	}
	return out
}

// renderProjectTaskLine renders one task row inside the project-detail
// pane. Same row format as the status view but without the project
// prefix (we already know which project — it's the section title).
func (m *Model) renderProjectTaskLine(r TaskRow, gs lipglossStyle, glyph string, cw colWidths, isCursor bool) string {
	cursor := "  "
	if isCursor {
		cursor = cursorStyle.Render("›") + " "
	}
	label := slugStyle.Render(r.Task.Slug)
	label = padToVisible(label, cw.label)

	pri := priorityStyleFor(r.Task.Priority).Render(priorityShort(r.Task.Priority))
	reltime := leftPadToVisible(mutedStyle.Render(r.RelTime), cw.relTime)

	var assignee string
	if cw.assignee > 0 {
		if r.Task.Assignee.Valid && r.Task.Assignee.String != "" {
			assignee = padToVisible(mutedStyle.Render("@"+r.Task.Assignee.String), cw.assignee+1)
		} else {
			assignee = strings.Repeat(" ", cw.assignee+1)
		}
	}

	parts := []string{cursor + gs.Render(glyph) + "  " + label}
	if assignee != "" {
		parts = append(parts, assignee)
	}
	parts = append(parts, pri, reltime)
	return strings.Join(parts, "   ")
}

func (m *Model) renderHeader() string {
	return titleStyle.Render("flow") + "   " +
		dateStyle.Render(m.snap.AsOf.Format("2006-01-02 15:04"))
}

// showSearchLine reports whether the dynamic search/filter row needs
// to render. When false, the View skips that row entirely and
// paneAllocations gains an extra line of pane content.
func (m *Model) showSearchLine() bool {
	return m.searching || !m.filter.empty()
}

// renderSearchLine renders the row between the counts pill and the
// panes. Two states (the row is suppressed entirely when neither is
// active — see showSearchLine):
//   - searching: shows "/  <input>   (N matches)"
//   - filter set (any axis): shows the active filter chips
func (m *Model) renderSearchLine() string {
	if m.searching {
		matchCount := len(m.snap.Working) + len(m.snap.Awaiting) +
			len(m.snap.Stale) + len(m.snap.Backlog)
		hint := paneCountStyle.Render(fmt.Sprintf("   (%d %s)",
			matchCount, plural(matchCount, "match", "matches")))
		return footerKeyStyle.Render("/  ") + m.search.View() + hint
	}
	if !m.filter.empty() {
		return m.renderFilterChips()
	}
	return ""
}

func (m *Model) renderFilterChips() string {
	var chips []string
	if m.filter.query != "" {
		chips = append(chips, paneCountStyle.Render("[/")+
			footerKeyStyle.Render(m.filter.query)+
			paneCountStyle.Render("]"))
	}
	if m.filter.priority != "" {
		chips = append(chips, paneCountStyle.Render("[")+
			priorityStyleFor(m.filter.priority).Render(priorityShort(m.filter.priority))+
			paneCountStyle.Render("]"))
	}
	if m.filter.assignee != "" {
		chips = append(chips, paneCountStyle.Render("[@")+
			footerKeyStyle.Render(m.filter.assignee)+
			paneCountStyle.Render("]"))
	}
	if m.filter.tag != "" {
		chips = append(chips, paneCountStyle.Render("[#")+
			footerKeyStyle.Render(m.filter.tag)+
			paneCountStyle.Render("]"))
	}
	if m.filter.project != "" {
		chips = append(chips, paneCountStyle.Render("[proj:")+
			footerKeyStyle.Render(m.filter.project)+
			paneCountStyle.Render("]"))
	}
	chips = append(chips, mutedStyle.Render("(esc clears)"))
	return strings.Join(chips, "  ")
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

func (m *Model) renderCounts() string {
	c := m.snap.Counts
	parts := []string{
		workingGlyphStyle.Render(GlyphWorking) + " " + fmt.Sprintf("%d working", c.Working),
		awaitingGlyphStyle.Render(GlyphAwaiting) + " " + fmt.Sprintf("%d awaiting", c.Awaiting),
		doneGlyphStyle.Render(GlyphDone) + " " + fmt.Sprintf("%d done (7d)", c.Done7d),
		backlogGlyphStyle.Render(GlyphBacklog) + " " + fmt.Sprintf("%d backlog", c.Backlog),
	}
	return strings.Join(parts, mutedStyle.Render("    "))
}

// visiblePanes returns the panes that have content, in display order.
// Empty panes are hidden so the layout doesn't waste vertical space
// on a "0 items" box.
func (m *Model) visiblePanes() []paneKind {
	out := make([]paneKind, 0, numPanes)
	for p := paneKind(0); p < numPanes; p++ {
		if m.paneRowCount(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// paneContentHeight returns the content-row allocation for pane p
// under the current terminal size and visible-pane mix. Adaptive:
// 1-or-2-row panes get exactly their row count; the remainder of the
// vertical space is divided among the bigger panes so a busy backlog
// doesn't have to share equal space with a 1-row stale pane.
func (m *Model) paneContentHeight(p paneKind) int {
	allocs := m.paneAllocations()
	if v, ok := allocs[p]; ok {
		return v
	}
	return 1
}

// paneAllocations computes the per-pane content-row allocation in one
// pass. Result is keyed by pane kind for every visible pane; absent
// panes are empty/hidden.
func (m *Model) paneAllocations() map[paneKind]int {
	visible := m.visiblePanes()
	n := len(visible)
	if n == 0 {
		return nil
	}
	const (
		bottomReserve = 3 // status + footer + safety
		perPaneChrome = 3 // title + 2 border lines
		minRows       = 2 // a pane below this is considered "small"
	)
	// Top reserve is dynamic: the search/filter line only renders when
	// search is open or a filter is set, so we reclaim that row (and
	// its trailing blank) for pane content otherwise.
	topReserve := 4 // header + blank + counts + blank
	if m.showSearchLine() {
		topReserve = 5 // + the search/filter line
	}
	h := m.height
	if h <= 0 {
		h = 24
	}
	avail := h - topReserve - bottomReserve - n*perPaneChrome
	if avail < n {
		avail = n
	}

	out := make(map[paneKind]int, n)
	bigPanes := make([]paneKind, 0, n)
	smallTotal := 0
	for _, p := range visible {
		rows := m.paneTotalLines(p)
		if rows <= minRows {
			give := rows
			if give < 1 {
				give = 1
			}
			out[p] = give
			smallTotal += give
		} else {
			bigPanes = append(bigPanes, p)
		}
	}

	leftover := avail - smallTotal
	if leftover < 0 {
		leftover = 0
	}
	if len(bigPanes) == 0 {
		return out
	}

	// First slice: equal split among big panes, capped by row count.
	share := leftover / len(bigPanes)
	if share < 1 {
		share = 1
	}
	for _, p := range bigPanes {
		rows := m.paneTotalLines(p)
		give := share
		if give > rows {
			give = rows
		}
		out[p] = give
	}

	// Redistribute slack: anything unused (because a big pane was
	// capped at its row count) goes to other big panes that still
	// want more.
	used := smallTotal
	for _, p := range bigPanes {
		used += out[p]
	}
	extra := avail - used
	for extra > 0 {
		distributed := false
		for _, p := range bigPanes {
			if out[p] < m.paneTotalLines(p) {
				out[p]++
				extra--
				distributed = true
				if extra <= 0 {
					break
				}
			}
		}
		if !distributed {
			break
		}
	}
	return out
}

// renderPaneWithWidth renders one pane (title line + bordered scrollable
// box) at the given outer width. innerWidth is outer minus 2 for the
// border characters.
func (m *Model) renderPaneWithWidth(p paneKind, outerWidth int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}

	rows := m.paneRowStrings(p, innerWidth)
	count := len(rows)
	contentH := m.paneContentHeight(p)

	state := &m.panes[p]
	maxOff := 0
	if count > contentH {
		maxOff = count - contentH
	}
	if state.scrollOffset > maxOff {
		state.scrollOffset = maxOff
	}
	if state.scrollOffset < 0 {
		state.scrollOffset = 0
	}

	end := state.scrollOffset + contentH
	if end > count {
		end = count
	}
	visible := rows[state.scrollOffset:end]
	for len(visible) < contentH {
		visible = append(visible, "")
	}

	body := make([]string, len(visible))
	for i, l := range visible {
		body[i] = padOrTrim(l, innerWidth)
	}

	isActive := p == m.active
	titleStyle := paneTitleStyle
	borderStyle := paneBorderStyle
	if isActive {
		titleStyle = paneActiveTitleStyle
		borderStyle = paneActiveBorderStyle
	}

	scrollHint := ""
	if count > contentH {
		scrollHint = paneCountStyle.Render(fmt.Sprintf("  [%d–%d / %d]",
			state.scrollOffset+1, end, count))
	}
	title := titleStyle.Render(paneTitles[p]) +
		paneCountStyle.Render(fmt.Sprintf("  (%d)", count)) + scrollHint

	box := borderStyle.Width(innerWidth).Render(strings.Join(body, "\n"))
	return title + "\n" + box
}

// renderPlaybookRightColumn renders the right column for the
// playbooks pane: top half is the playbook's metadata, bottom half
// lists the runs of that playbook. The two stack vertically and
// together occupy the same height as the left column.
func (m *Model) renderPlaybookRightColumn(outerWidth int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	totalH := m.detailContentHeight()
	// Split: detail gets the larger half (more metadata than runs lines).
	detailH := totalH * 55 / 100
	runsH := totalH - detailH - 3 // 3 chrome for the runs pane
	if detailH < 6 {
		detailH = 6
	}
	if runsH < 3 {
		runsH = 3
	}

	return m.renderPlaybookDetail(outerWidth, detailH) + "\n" +
		m.renderRunsPane(outerWidth, runsH)
}

// renderPlaybookDetail is the playbook variant of renderDetailPane.
func (m *Model) renderPlaybookDetail(outerWidth, contentH int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	body := m.playbookDetailBody(innerWidth)
	lines := strings.Split(body, "\n")
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padOrTrim(l, innerWidth)
	}
	title := paneTitleStyle.Render("playbook")
	box := paneBorderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
	return title + "\n" + box
}

// playbookDetailBody composes the top right-pane content for the
// currently selected playbook (or a hint when nothing is selected).
func (m *Model) playbookDetailBody(innerWidth int) string {
	row := m.selectedPlaybook()
	if row == nil {
		return mutedStyle.Render("Select a playbook with ↑↓ to see details.")
	}
	pb := row.Playbook

	var b strings.Builder
	heading := slugStyle.Render(pb.Slug)
	if row.ProjectSlug != "" {
		heading = projectStyle.Render(row.ProjectSlug+"/") + heading
	}
	b.WriteString(heading + "\n")
	if pb.Name != "" && pb.Name != pb.Slug {
		b.WriteString(wrap(mutedStyle.Render(pb.Name), innerWidth) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(metaLine("runs", fmt.Sprintf("%d", row.RunCount)) + "\n")
	if row.LastRunAgo != "" {
		b.WriteString(metaLine("last run", row.LastRunAgo) + "\n")
	}
	if pb.WorkDir != "" {
		b.WriteString(metaLine("workdir", pb.WorkDir) + "\n")
	}

	if row.Headline != "" {
		b.WriteString("\n")
		b.WriteString(sectionStyle.Render("latest update") + "\n")
		b.WriteString(wrap(row.Headline, innerWidth))
	}
	return b.String()
}

// renderRunsPane renders the runs sub-pane under the playbook detail.
// Highlights its border + cursor when m.runsFocused is true so the
// user can see which sub-pane keystrokes are going to.
func (m *Model) renderRunsPane(outerWidth, contentH int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	row := m.selectedPlaybook()
	var lines []string
	count := 0
	if row != nil {
		count = len(row.Runs)
	}
	if row == nil || count == 0 {
		lines = []string{mutedStyle.Render("no runs yet — `flow run playbook <slug>` creates one")}
	} else {
		cw := computeRunColWidths(row.Runs)
		cursor := -1
		if m.runsFocused {
			cursor = m.runCursor
		}
		for i, r := range row.Runs {
			lines = append(lines, m.renderRunLine(r, i == cursor, cw))
		}
	}
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padOrTrim(l, innerWidth)
	}

	titleStyle := paneTitleStyle
	borderStyle := paneBorderStyle
	if m.runsFocused {
		titleStyle = paneActiveTitleStyle
		borderStyle = paneActiveBorderStyle
	}
	title := titleStyle.Render("runs") +
		paneCountStyle.Render(fmt.Sprintf("  (%d)", count))
	box := borderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
	return title + "\n" + box
}

// runColWidths is the column width for run rows inside the playbook
// runs sub-pane. slug is the widest run-slug in the list (the reltime
// column stays right-of-slug; no need to pad).
type runColWidths struct {
	slug int
}

func computeRunColWidths(rows []TaskRow) runColWidths {
	var cw runColWidths
	for _, r := range rows {
		if l := len(r.Task.Slug); l > cw.slug {
			cw.slug = l
		}
	}
	return cw
}

func (m *Model) renderRunLine(r TaskRow, isCursor bool, cw runColWidths) string {
	t := r.Task
	cursor := "  "
	if isCursor {
		cursor = cursorStyle.Render("›") + " "
	}
	var glyph string
	var gs lipglossStyle
	switch t.Status {
	case "in-progress":
		glyph, gs = GlyphWorking, workingGlyphStyle
	case "done":
		glyph, gs = GlyphDone, doneGlyphStyle
	default:
		glyph, gs = GlyphBacklog, backlogGlyphStyle
	}
	slug := padToVisible(slugStyle.Render(t.Slug), cw.slug)
	return fmt.Sprintf("%s%s  %s   %s",
		cursor,
		gs.Render(glyph),
		slug,
		leftPadToVisible(mutedStyle.Render(r.RelTime), 7),
	)
}

// renderDetailPane renders the right-side detail panel showing the
// fully-formatted task selected in the active task pane.
func (m *Model) renderDetailPane(outerWidth int) string {
	innerWidth := outerWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	contentH := m.detailContentHeight()

	body := m.detailBody(innerWidth)
	lines := strings.Split(body, "\n")
	if len(lines) > contentH {
		lines = lines[:contentH]
	}
	for len(lines) < contentH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = padOrTrim(l, innerWidth)
	}

	title := paneTitleStyle.Render("details")
	box := paneBorderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
	return title + "\n" + box
}

func (m *Model) detailContentHeight() int {
	// Match the visual height of the left column so JoinHorizontal
	// aligns clean rectangles. Left column = N * (contentH + 3) where
	// Each pane occupies (allocated content rows + 3 chrome) lines;
	// the detail pane has its own (1 title + 2 borders) chrome that
	// we subtract from the left total to size its content viewport.
	visible := m.visiblePanes()
	if len(visible) == 0 {
		return 1
	}
	allocs := m.paneAllocations()
	leftTotal := 0
	for _, p := range visible {
		leftTotal += allocs[p] + 3
	}
	return leftTotal - 3
}

// detailBody composes the right-pane content for the currently selected
// task row (or a hint when nothing is selected).
func (m *Model) detailBody(innerWidth int) string {
	row := m.selectedTaskRow()
	if row == nil {
		return mutedStyle.Render("Select a task with ←→ ↑↓ to see details.")
	}
	t := row.Task

	var b strings.Builder
	// Heading: project / slug, with the human-readable name below.
	heading := slugStyle.Render(t.Slug)
	if row.ProjectSlug != "" {
		heading = projectStyle.Render(row.ProjectSlug+"/") + heading
	}
	b.WriteString(heading + "\n")
	if t.Name != "" && t.Name != t.Slug {
		b.WriteString(wrap(mutedStyle.Render(t.Name), innerWidth) + "\n")
	}
	b.WriteString("\n")

	// Metadata.
	b.WriteString(metaLine("priority", t.Priority) + "\n")
	b.WriteString(metaLine("status", t.Status) + "\n")
	b.WriteString(metaLine("updated", row.RelTime) + "\n")
	if row.CreatedAgo != "" {
		b.WriteString(metaLine("created", row.CreatedAgo) + "\n")
	}
	if t.DueDate.Valid && t.DueDate.String != "" {
		b.WriteString(metaLine("due", t.DueDate.String) + "\n")
	}
	if t.Assignee.Valid && t.Assignee.String != "" {
		b.WriteString(metaLine("assignee", "@"+t.Assignee.String) + "\n")
	}
	if t.WaitingOn.Valid && t.WaitingOn.String != "" {
		b.WriteString(waitingContextStyle.Render(rightPad("waiting", 10)+t.WaitingOn.String) + "\n")
	}
	if t.WorkDir != "" {
		b.WriteString(metaLine("workdir", t.WorkDir) + "\n")
	}
	if len(row.Tags) > 0 {
		hashed := make([]string, len(row.Tags))
		for i, tag := range row.Tags {
			hashed[i] = "#" + tag
		}
		tagsLine := wrap(strings.Join(hashed, " "), innerWidth-10)
		b.WriteString(metaLine("tags", "") + tagsLine + "\n")
	}

	// Latest update headline.
	if row.Headline != "" {
		b.WriteString("\n")
		b.WriteString(sectionStyle.Render("latest update") + "\n")
		b.WriteString(wrap(row.Headline, innerWidth))
	}
	return b.String()
}

func metaLine(label, value string) string {
	return mutedStyle.Render(rightPad(label, 10)) + value
}

// selectedTaskRow returns the row at the cursor in the active task
// pane, or nil when the active pane is the activity log, the
// playbooks pane, or empty.
func (m *Model) selectedTaskRow() *TaskRow {
	rows := m.paneTaskRows(m.active)
	if len(rows) == 0 {
		return nil
	}
	c := m.panes[m.active].cursor
	if c < 0 || c >= len(rows) {
		return nil
	}
	return &rows[c]
}

// selectedPlaybook returns the playbook row at the cursor when the
// active pane is panePlaybooks. Used by the right column to render
// the playbook detail + runs sub-pane.
func (m *Model) selectedPlaybook() *PlaybookRow {
	if m.active != panePlaybooks {
		return nil
	}
	if m.snap == nil || len(m.snap.Playbooks) == 0 {
		return nil
	}
	c := m.panes[panePlaybooks].cursor
	if c < 0 || c >= len(m.snap.Playbooks) {
		return nil
	}
	return &m.snap.Playbooks[c]
}

// wrap word-wraps s to width n. Returns the input unchanged if n is
// non-positive.
func wrap(s string, n int) string {
	if n <= 0 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if len(line) <= n {
			out = append(out, line)
			continue
		}
		var cur string
		for _, w := range strings.Fields(line) {
			switch {
			case cur == "":
				cur = w
			case len(cur)+1+len(w) <= n:
				cur += " " + w
			default:
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return strings.Join(out, "\n")
}

// paneRowStrings returns the pre-rendered lines for pane p. Task rows
// that overflow the available label width wrap onto continuation
// lines, so the returned slice has one entry per rendered line (not
// per row). Only the active task pane shows the cursor marker.
func (m *Model) paneRowStrings(p paneKind, innerWidth int) []string {
	switch p {
	case paneActivity:
		rows := m.snap.Activity
		out := make([]string, 0, len(rows))
		for _, e := range rows {
			out = append(out, m.renderEvent(e))
		}
		return out
	case panePlaybooks:
		rows := m.snap.Playbooks
		cw := computePlaybookColWidths(rows, innerWidth)
		cursor := -1
		if m.active == p {
			cursor = m.panes[p].cursor
		}
		out := make([]string, 0, len(rows))
		for i, r := range rows {
			out = append(out, m.renderPlaybookRow(r, i == cursor, cw))
		}
		return out
	}

	rows := m.paneTaskRows(p)
	cw := m.taskColWidths(innerWidth)
	cursor := -1
	if m.active == p {
		cursor = m.panes[p].cursor
	}
	gs, g := paneGlyph(p)
	out := make([]string, 0, len(rows))
	for i, r := range rows {
		out = append(out, m.renderRow(r, gs, g, i == cursor, cw)...)
	}
	return out
}

// taskColWidths returns column widths computed across every task pane,
// so a row in `working` lines up with a row in `backlog` (same label
// padding, same assignee/reltime columns). Playbook rows have their
// own widths via computePlaybookColWidths.
func (m *Model) taskColWidths(innerWidth int) colWidths {
	if m.snap == nil {
		return colWidths{}
	}
	all := make([]TaskRow, 0,
		len(m.snap.Working)+len(m.snap.Awaiting)+len(m.snap.Stale)+len(m.snap.Backlog))
	all = append(all, m.snap.Working...)
	all = append(all, m.snap.Awaiting...)
	all = append(all, m.snap.Stale...)
	all = append(all, m.snap.Backlog...)
	return computeColWidths(all, innerWidth)
}

// playbookColWidths is the per-pane visible width for each playbook
// row column. Computed across the rows in the pane so every row's
// run-count and last-run columns line up vertically.
type playbookColWidths struct {
	label    int
	runCount int
}

func computePlaybookColWidths(rows []PlaybookRow, innerWidth int) playbookColWidths {
	const lastRunReserve = 12 // "never run" / "21h ago" / "2026-05-15"
	var cw playbookColWidths
	for _, r := range rows {
		labelLen := len(r.Playbook.Slug)
		if r.ProjectSlug != "" {
			labelLen = len(r.ProjectSlug) + 1 + len(r.Playbook.Slug)
		}
		if labelLen > cw.label {
			cw.label = labelLen
		}
		runStr := fmt.Sprintf("%d runs", r.RunCount)
		if len(runStr) > cw.runCount {
			cw.runCount = len(runStr)
		}
	}
	budget := innerWidth - rowLeftChrome - rowMidGap - cw.runCount - rowRightGap - lastRunReserve
	if budget < 10 {
		budget = 10
	}
	if cw.label > budget {
		cw.label = budget
	}
	return cw
}

// renderPlaybookRow renders a row in the playbooks pane with project/
// slug + run count + last-run-ago. Label and run count are padded to
// pane-wide column widths so the columns align across rows.
func (m *Model) renderPlaybookRow(r PlaybookRow, isCursor bool, cw playbookColWidths) string {
	cursor := "  "
	if isCursor {
		cursor = cursorStyle.Render("›") + " "
	}

	label := slugStyle.Render(r.Playbook.Slug)
	if r.ProjectSlug != "" {
		label = projectStyle.Render(r.ProjectSlug+"/") + label
	}
	label = padToVisible(label, cw.label)

	runCount := padToVisible(mutedStyle.Render(fmt.Sprintf("%d runs", r.RunCount)), cw.runCount)

	lastRun := r.LastRunAgo
	if lastRun == "" {
		lastRun = "never run"
	}

	return fmt.Sprintf("%s%s  %s   %s   %s",
		cursor,
		playbookGlyphStyle.Render(GlyphPlaybook),
		label,
		runCount,
		mutedStyle.Render(lastRun),
	)
}

// colWidths is the per-pane visible width for each column that varies
// across rows. assignee column is 0 when no row in the pane has one,
// which suppresses the column entirely.
type colWidths struct {
	label    int
	assignee int
	relTime  int
}

// Row layout constants. Kept in one place because row rendering AND
// row line-counting (for scroll math) both depend on them.
const (
	rowLeftChrome = 5 // cursor(2) + glyph(1) + 2 spaces
	rowMidGap     = 3 // between label and priority
	rowPriWidth   = 2 // "hi" / "me" / "lo"
	rowRightGap   = 3 // between priority and reltime
)

// computeColWidths picks label and reltime column widths so the
// priority/reltime fields line up across rows in a pane. Label width
// grows to the widest row but is capped to whatever fits the pane —
// rows that exceed the cap wrap onto continuation lines.
func computeColWidths(rows []TaskRow, innerWidth int) colWidths {
	var cw colWidths
	for _, r := range rows {
		labelLen := plainLabelLen(r)
		if labelLen > cw.label {
			cw.label = labelLen
		}
		if len(r.RelTime) > cw.relTime {
			cw.relTime = len(r.RelTime)
		}
		if r.Task.Assignee.Valid {
			if l := len(r.Task.Assignee.String); l > cw.assignee {
				cw.assignee = l
			}
		}
	}
	// Reserve enough horizontal space for everything to the right of
	// the label column; whatever's left is the label budget.
	rightWidth := rowPriWidth + rowRightGap + cw.relTime
	if cw.assignee > 0 {
		rightWidth += cw.assignee + rowRightGap
	}
	budget := innerWidth - rowLeftChrome - rowMidGap - rightWidth
	if budget < 10 {
		budget = 10
	}
	if cw.label > budget {
		cw.label = budget
	}
	return cw
}

// plainLabelLen returns the unstyled visible length of a row's label
// (project/slug or just slug). Used to compute column widths and
// per-row line count consistently.
func plainLabelLen(r TaskRow) int {
	if r.ProjectSlug != "" {
		return len(r.ProjectSlug) + 1 + len(r.Task.Slug)
	}
	return len(r.Task.Slug)
}

func paneGlyph(p paneKind) (lipglossStyle, string) {
	switch p {
	case paneWorking:
		return workingGlyphStyle, GlyphWorking
	case paneAwaiting:
		return awaitingGlyphStyle, GlyphAwaiting
	case paneStale:
		return staleGlyphStyle, GlyphStale
	case paneBacklog:
		return backlogGlyphStyle, GlyphBacklog
	}
	return mutedStyle, ""
}

type lipglossStyle = interface{ Render(strs ...string) string }

// renderRow returns one rendered line for a task row. Labels that
// exceed cw.label are truncated with an ellipsis so priority,
// assignee, and reltime always render at full width — those columns
// are the most useful at a glance and shouldn't get clipped just
// because a slug is long.
func (m *Model) renderRow(r TaskRow, glyphStyle lipglossStyle, glyph string, isCursor bool, cw colWidths) []string {
	cursor := "  "
	if isCursor {
		cursor = cursorStyle.Render("›") + " "
	}

	plain := r.Task.Slug
	slashIdx := -1
	if r.ProjectSlug != "" {
		plain = r.ProjectSlug + "/" + r.Task.Slug
		slashIdx = len(r.ProjectSlug)
	}

	var matches []int
	if m.filter.query != "" {
		matches = fuzzyMatchPositions(m.filter.query, plain)
	}

	label := styleLabelTruncated(plain, slashIdx, cw.label, matches)
	label = padToVisible(label, cw.label)

	pri := priorityStyleFor(r.Task.Priority).Render(priorityShort(r.Task.Priority))
	// Right-align reltime so "1d ago" and "12d ago" align at the right
	// edge of the column instead of "1d ago " trailing into a gap.
	reltime := leftPadToVisible(mutedStyle.Render(r.RelTime), cw.relTime)

	var assignee string
	if cw.assignee > 0 {
		if r.Task.Assignee.Valid && r.Task.Assignee.String != "" {
			assignee = padToVisible(mutedStyle.Render("@"+r.Task.Assignee.String), cw.assignee+1)
		} else {
			assignee = strings.Repeat(" ", cw.assignee+1)
		}
	}

	parts := []string{cursor + glyphStyle.Render(glyph) + "  " + label}
	if assignee != "" {
		parts = append(parts, assignee)
	}
	parts = append(parts, pri, reltime)
	return []string{strings.Join(parts, "   ")}
}

// styleLabelTruncated styles a label that fits within maxVisible
// columns. If the underlying plain string is longer, it cuts and
// appends "…" (1 visible column) so the total visible width is
// exactly maxVisible. Project/slug colors and search highlights
// apply to whatever portion of the label remains visible.
func styleLabelTruncated(plain string, slashIdx, maxVisible int, matchPos []int) string {
	if maxVisible <= 0 {
		return ""
	}
	if len(plain) <= maxVisible {
		return styleLabelChunk(plain, 0, slashIdx, matchPos)
	}
	if maxVisible == 1 {
		return mutedStyle.Render("…")
	}
	head := plain[:maxVisible-1]
	return styleLabelChunk(head, 0, slashIdx, matchPos) + mutedStyle.Render("…")
}

// styleLabelChunk applies project/slug coloring to a wrap-chunk of the
// plain label, given the chunk's byte offset in the plain label and
// the position of the project/slug slash (-1 = no project). matchPos
// is a sorted set of plain-label byte indices that the active search
// query matched; characters at those indices render with
// matchHighlightStyle instead of the project/slug tint.
func styleLabelChunk(chunk string, offset, slashIdx int, matchPos []int) string {
	if len(matchPos) == 0 {
		// Fast path: no highlights to apply.
		if slashIdx < 0 {
			return slugStyle.Render(chunk)
		}
		boundary := slashIdx + 1
		end := offset + len(chunk)
		switch {
		case end <= boundary:
			return projectStyle.Render(chunk)
		case offset >= boundary:
			return slugStyle.Render(chunk)
		default:
			split := boundary - offset
			return projectStyle.Render(chunk[:split]) + slugStyle.Render(chunk[split:])
		}
	}

	// Build a per-char set of which local positions in the chunk are
	// matched. Wrap chunks share a flat plain-label index space, so
	// match positions translate as (absolute - offset).
	matchSet := make(map[int]bool, len(matchPos))
	for _, p := range matchPos {
		if p >= offset && p < offset+len(chunk) {
			matchSet[p-offset] = true
		}
	}

	boundary := slashIdx + 1
	var b strings.Builder
	for i := 0; i < len(chunk); i++ {
		var style = slugStyle
		if slashIdx >= 0 && offset+i < boundary {
			style = projectStyle
		}
		if matchSet[i] {
			style = matchHighlightStyle
		}
		b.WriteString(style.Render(string(chunk[i])))
	}
	return b.String()
}

// padToVisible right-pads a possibly-ANSI-styled string to n visible
// columns. Used by renderRow to align label/reltime across rows in
// the same pane; no-op when already at or past n.
func padToVisible(s string, n int) string {
	vl := visibleLen(s)
	if vl >= n {
		return s
	}
	return s + strings.Repeat(" ", n-vl)
}

// leftPadToVisible right-aligns by prepending spaces. Used for the
// reltime column so "1d ago" and "12d ago" line up at the column's
// right edge across rows.
func leftPadToVisible(s string, n int) string {
	vl := visibleLen(s)
	if vl >= n {
		return s
	}
	return strings.Repeat(" ", n-vl) + s
}

// indentBlock prefixes every line of s with n spaces. Used to push
// the dashboard's content column off the terminal's left edge while
// preserving the structure produced by lipgloss.JoinHorizontal.
func indentBlock(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

func (m *Model) renderEvent(e Event) string {
	target := slugStyle.Render(e.TargetSlug)
	if e.TargetProj != "" {
		target = projectStyle.Render(e.TargetProj+"/") + target
	}
	return fmt.Sprintf("  %s   %s   %s   %s",
		hashStyle.Render(e.Hash),
		mutedStyle.Render(rightPad(e.RelTime, 4)),
		mutedStyle.Render(rightPad(e.Kind, 14)),
		target,
	)
}

func (m *Model) footer() string {
	if m.searching {
		parts := []string{
			footerKeyStyle.Render("type") + " filter",
			footerKeyStyle.Render("enter") + " keep",
			footerKeyStyle.Render("esc") + " cancel",
		}
		return "  " + footerStyle.Render(strings.Join(parts, " · "))
	}
	if m.view == viewByProject {
		var parts []string
		if m.projectTaskFocus {
			parts = []string{
				footerKeyStyle.Render("↑↓") + " task",
				footerKeyStyle.Render("enter") + " open",
				footerKeyStyle.Render("←/esc") + " back",
				footerKeyStyle.Render("v") + " status view",
				footerKeyStyle.Render("q") + " quit",
			}
		} else {
			parts = []string{
				footerKeyStyle.Render("↑↓") + " project",
				footerKeyStyle.Render("→") + " open tasks",
				footerKeyStyle.Render("v") + " status view",
				footerKeyStyle.Render("/") + " search",
				footerKeyStyle.Render("q") + " quit",
			}
		}
		return "  " + footerStyle.Render(strings.Join(parts, " · "))
	}
	if m.runsFocused {
		parts := []string{
			footerKeyStyle.Render("↑↓") + " run",
			footerKeyStyle.Render("enter") + " open run",
			footerKeyStyle.Render("←/esc") + " back",
			footerKeyStyle.Render("q") + " quit",
		}
		return "  " + footerStyle.Render(strings.Join(parts, " · "))
	}
	if m.active == panePlaybooks {
		parts := []string{
			footerKeyStyle.Render("↑↓") + " playbook",
			footerKeyStyle.Render("enter") + " new run",
			footerKeyStyle.Render("→") + " open existing run",
			footerKeyStyle.Render("tab") + " pane",
			footerKeyStyle.Render("q") + " quit",
		}
		return "  " + footerStyle.Render(strings.Join(parts, " · "))
	}
	parts := []string{
		footerKeyStyle.Render("↑↓") + " select",
		footerKeyStyle.Render("←→") + " pane",
		footerKeyStyle.Render("enter") + " open",
		footerKeyStyle.Render("/") + " search",
		footerKeyStyle.Render("p/P/a/t") + " filter",
		footerKeyStyle.Render("v") + " project view",
		footerKeyStyle.Render("q") + " quit",
	}
	return "  " + footerStyle.Render(strings.Join(parts, " · "))
}

// ----- string helpers -----

func rightPad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// padOrTrim ensures a line is exactly n visible columns wide. Trims
// long content (preserving any open ANSI styles by appending a reset)
// and pads short content with spaces. This is the critical guard
// against lipgloss wrapping a long row onto multiple visible lines,
// which would balloon the pane's rendered height beyond its viewport.
func padOrTrim(s string, n int) string {
	vl := visibleLen(s)
	if vl > n {
		return truncateANSI(s, n)
	}
	if vl == n {
		return s
	}
	return s + strings.Repeat(" ", n-vl)
}

// truncateANSI returns s truncated to a visible width of n, leaving
// ANSI escape sequences intact. Always emits a SGR reset at the end
// when any style was opened, so subsequent output isn't styled by
// the truncated prefix.
func truncateANSI(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	count := 0
	inEsc := false
	hadStyle := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
				if r == 'm' {
					hadStyle = true
				}
			}
			continue
		}
		if r == '\x1b' {
			b.WriteRune(r)
			inEsc = true
			continue
		}
		if count >= n {
			break
		}
		b.WriteRune(r)
		count++
	}
	if hadStyle {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// visibleLen returns the visible (non-ANSI) length of a string. Strips
// CSI escape sequences (\x1b[…m).
func visibleLen(s string) int {
	count := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' || r == 'K' || r == 'H' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		count++
	}
	return count
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ----- entry point -----

// Run opens the DB at dbPath, builds a Model, and runs the bubbletea
// program in alt-screen mode until the user quits.
func Run(dbPath, flowRoot string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	flowBin, err := exec.LookPath("flow")
	if err != nil {
		return fmt.Errorf("flow binary not found on PATH: %w", err)
	}

	loader := &Loader{DB: db, FlowRoot: flowRoot}
	m := NewModel(loader, flowBin)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	return nil
}
