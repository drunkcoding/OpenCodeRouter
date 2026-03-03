package components

import (
	"fmt"
	"strings"

	"opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/theme"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

type treeNodeKind int

const (
	treeNodeHost treeNodeKind = iota
	treeNodeProject
	treeNodeSession
)

type treeRow struct {
	kind          treeNodeKind
	hostIdx       int
	projectIdx    int
	sessionIdx    int
	hostCollapsed bool
	projCollapsed bool
}

// SessionTreeView renders Host → Project → Session hierarchy with collapsing.
type SessionTreeView struct {
	hosts             []model.Host
	filterQuery       string
	collapsedHosts    map[string]bool
	collapsedProjects map[string]bool
	rows              []treeRow
	cursor            int
	scroll            int
	width             int
	height            int
	theme             theme.Theme
}

// NewSessionTreeView creates a new hierarchy view.
func NewSessionTreeView(th theme.Theme) SessionTreeView {
	return SessionTreeView{
		collapsedHosts:    make(map[string]bool),
		collapsedProjects: make(map[string]bool),
		theme:             th,
	}
}

// SetSize applies viewport dimensions.
func (t *SessionTreeView) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.rebuild()
}

// SetHosts updates the host tree data.
func (t *SessionTreeView) SetHosts(hosts []model.Host) {
	t.hosts = append([]model.Host(nil), hosts...)
	t.rebuild()
}

// SetFilter updates search filter and recomputes visible rows.
func (t *SessionTreeView) SetFilter(query string) {
	t.filterQuery = strings.ToLower(strings.TrimSpace(query))
	t.rebuild()
}

// Hosts returns the currently loaded hosts.
func (t SessionTreeView) Hosts() []model.Host {
	return append([]model.Host(nil), t.hosts...)
}

// Selected returns currently selected host/project/session nodes.
func (t *SessionTreeView) Selected() (*model.Host, *model.Project, *model.Session, bool) {
	if len(t.rows) == 0 || t.cursor < 0 || t.cursor >= len(t.rows) {
		return nil, nil, nil, false
	}

	row := t.rows[t.cursor]
	h := &t.hosts[row.hostIdx]

	switch row.kind {
	case treeNodeHost:
		return h, nil, nil, true
	case treeNodeProject:
		if row.projectIdx < 0 || row.projectIdx >= len(h.Projects) {
			return nil, nil, nil, false
		}
		p := &h.Projects[row.projectIdx]
		return h, p, nil, true
	case treeNodeSession:
		if row.projectIdx < 0 || row.projectIdx >= len(h.Projects) {
			return nil, nil, nil, false
		}
		p := &h.Projects[row.projectIdx]
		if row.sessionIdx < 0 || row.sessionIdx >= len(p.Sessions) {
			return nil, nil, nil, false
		}
		s := &p.Sessions[row.sessionIdx]
		return h, p, s, true
	default:
		return nil, nil, nil, false
	}
}

// Given a key message, when Update runs, then navigation and collapse state are reconciled.
func (t SessionTreeView) Update(msg tea.Msg) (SessionTreeView, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return t, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if t.cursor > 0 {
			t.cursor--
		}
	case "down", "j":
		if t.cursor < len(t.rows)-1 {
			t.cursor++
		}
	case "enter", " ":
		t.toggleAtCursor()
	case "left", "h":
		t.collapseAtCursor()
	case "right", "l":
		t.expandAtCursor()
	}

	t.clampScroll()
	return t, nil
}

// View renders the tree panel.
func (t SessionTreeView) View() string {
	if len(t.rows) == 0 {
		empty := t.theme.TreeMuted.Render("No matching hosts")
		if t.width > 0 {
			empty = lipgloss.NewStyle().Width(treeMaxInt(0, t.width-2)).Render(empty)
		}
		return t.theme.Tree.Render(empty)
	}

	start := t.scroll
	if start < 0 {
		start = 0
	}
	end := len(t.rows)
	if t.height > 0 && start+t.height < end {
		end = start + t.height
	}

	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		line := t.renderRow(t.rows[i], i == t.cursor)
		if t.width > 0 {
			line = lipgloss.NewStyle().Width(treeMaxInt(0, t.width-2)).Render(line)
		}
		lines = append(lines, line)
	}

	return t.theme.Tree.Render(strings.Join(lines, "\n"))
}

func (t *SessionTreeView) rebuild() {
	t.rows = t.rows[:0]
	query := t.filterQuery

	for hi, host := range t.hosts {
		if !hostMatchesQuery(host, query) {
			continue
		}
		hostKey := host.Name
		hostCollapsed := t.collapsedHosts[hostKey]
		t.rows = append(t.rows, treeRow{kind: treeNodeHost, hostIdx: hi, projectIdx: -1, sessionIdx: -1, hostCollapsed: hostCollapsed})
		if hostCollapsed {
			continue
		}

		for pi, project := range host.Projects {
			if !projectMatchesQuery(host, project, query) {
				continue
			}
			projectKey := makeProjectKey(host.Name, project.Name)
			projectCollapsed := t.collapsedProjects[projectKey]
			t.rows = append(t.rows, treeRow{kind: treeNodeProject, hostIdx: hi, projectIdx: pi, sessionIdx: -1, projCollapsed: projectCollapsed})
			if projectCollapsed {
				continue
			}

			for si, session := range project.Sessions {
				if !sessionMatchesQuery(host, project, session, query) {
					continue
				}
				t.rows = append(t.rows, treeRow{kind: treeNodeSession, hostIdx: hi, projectIdx: pi, sessionIdx: si})
			}
		}
	}

	if t.cursor >= len(t.rows) {
		t.cursor = len(t.rows) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.clampScroll()
}

func (t SessionTreeView) renderRow(row treeRow, selected bool) string {
	h := t.hosts[row.hostIdx]
	var line string

	switch row.kind {
	case treeNodeHost:
		glyph := "▾"
		if t.collapsedHosts[h.Name] {
			glyph = "▸"
		}
		status := string(h.Status)
		if status == "" {
			status = string(model.HostStatusUnknown)
		}
		line = fmt.Sprintf("%s %s [%s]", glyph, h.Label, status)
		line = t.theme.TreeHost.Render(line)
	case treeNodeProject:
		p := h.Projects[row.projectIdx]
		glyph := "▾"
		if t.collapsedProjects[makeProjectKey(h.Name, p.Name)] {
			glyph = "▸"
		}
		line = fmt.Sprintf("  %s %s", glyph, p.Name)
		line = t.theme.TreeProject.Render(line)
	case treeNodeSession:
		p := h.Projects[row.projectIdx]
		s := p.Sessions[row.sessionIdx]
		title := s.Title
		if strings.TrimSpace(title) == "" {
			title = s.ID
		}
		line = fmt.Sprintf("    • %s (%s)", title, s.Activity)
		line = t.theme.TreeSession.Render(line)
	}

	if selected {
		line = t.theme.TreeCursor.Render(line)
	}
	return line
}

func (t *SessionTreeView) toggleAtCursor() {
	if len(t.rows) == 0 {
		return
	}
	row := t.rows[t.cursor]
	switch row.kind {
	case treeNodeHost:
		hostName := t.hosts[row.hostIdx].Name
		t.collapsedHosts[hostName] = !t.collapsedHosts[hostName]
	case treeNodeProject:
		h := t.hosts[row.hostIdx]
		p := h.Projects[row.projectIdx]
		projectKey := makeProjectKey(h.Name, p.Name)
		t.collapsedProjects[projectKey] = !t.collapsedProjects[projectKey]
	}
	t.rebuild()
}

func (t *SessionTreeView) collapseAtCursor() {
	if len(t.rows) == 0 {
		return
	}
	row := t.rows[t.cursor]
	switch row.kind {
	case treeNodeHost:
		hostName := t.hosts[row.hostIdx].Name
		t.collapsedHosts[hostName] = true
	case treeNodeProject:
		h := t.hosts[row.hostIdx]
		p := h.Projects[row.projectIdx]
		t.collapsedProjects[makeProjectKey(h.Name, p.Name)] = true
	}
	t.rebuild()
}

func (t *SessionTreeView) expandAtCursor() {
	if len(t.rows) == 0 {
		return
	}
	row := t.rows[t.cursor]
	switch row.kind {
	case treeNodeHost:
		hostName := t.hosts[row.hostIdx].Name
		t.collapsedHosts[hostName] = false
	case treeNodeProject:
		h := t.hosts[row.hostIdx]
		p := h.Projects[row.projectIdx]
		t.collapsedProjects[makeProjectKey(h.Name, p.Name)] = false
	}
	t.rebuild()
}

func (t *SessionTreeView) clampScroll() {
	if t.height <= 0 || len(t.rows) <= t.height {
		t.scroll = 0
		return
	}
	if t.cursor < t.scroll {
		t.scroll = t.cursor
	}
	if t.cursor >= t.scroll+t.height {
		t.scroll = t.cursor - t.height + 1
	}
	if t.scroll < 0 {
		t.scroll = 0
	}
	maxScroll := len(t.rows) - t.height
	if t.scroll > maxScroll {
		t.scroll = maxScroll
	}
}

func makeProjectKey(host, project string) string {
	return host + "::" + project
}

func hostMatchesQuery(host model.Host, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(host.Name), query) || strings.Contains(strings.ToLower(host.Label), query) {
		return true
	}
	for _, project := range host.Projects {
		if projectMatchesQuery(host, project, query) {
			return true
		}
	}
	return false
}

func projectMatchesQuery(host model.Host, project model.Project, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(project.Name), query) || strings.Contains(strings.ToLower(host.Name), query) || strings.Contains(strings.ToLower(host.Label), query) {
		return true
	}
	for _, session := range project.Sessions {
		if sessionMatchesQuery(host, project, session, query) {
			return true
		}
	}
	return false
}

func sessionMatchesQuery(host model.Host, project model.Project, session model.Session, query string) bool {
	if query == "" {
		return true
	}
	joined := strings.ToLower(strings.Join([]string{host.Name, host.Label, project.Name, session.ID, session.Title}, " "))
	return strings.Contains(joined, query)
}

func treeMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
