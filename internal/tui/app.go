package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path"
	"strings"
	"time"

	"opencoderouter/internal/tui/components"
	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/discovery"
	"opencoderouter/internal/tui/keys"
	"opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/probe"
	"opencoderouter/internal/tui/theme"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// Discoverer resolves hosts from local SSH configuration.
type Discoverer interface {
	Discover(ctx context.Context) ([]model.Host, error)
}

// Prober collects sessions from hosts via SSH probes.
type Prober interface {
	ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error)
}

// AppModel is the top-level Bubble Tea model for opencode-remote.
type AppModel struct {
	cfg    config.Config
	theme  theme.Theme
	keys   keys.KeyMap
	logger *slog.Logger

	discovery Discoverer
	prober    Prober

	header  components.HeaderBar
	tree    components.SessionTreeView
	inspect components.InspectPanel
	footer  components.FooterHelpBar
	toast   components.InlineToast
	modal   components.ModalLayer
	spinner components.BrailleSpinner

	hosts       []model.Host
	lastError   error
	nextRefresh time.Time
	width       int
	height      int
	showInspect bool
}

const (
	errorToastTimeout      = 5 * time.Second
	maxSanitizedErrorRunes = 320
)

// NewApp constructs the root model with injected services.
func NewApp(cfg config.Config, discoverer Discoverer, proberSvc Prober, logger *slog.Logger) *AppModel {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	appLogger := logger.With("component", "app")
	probeLogger := logger.With("component", "probe")
	discoveryLogger := logger.With("component", "discovery")

	th := theme.ByName(cfg.Display.Theme)
	keyMap := keys.NewKeyMap(cfg.Keybindings)

	if discoverer == nil {
		discoverer = discovery.NewDiscoveryService(cfg, nil, discoveryLogger)
	}
	if proberSvc == nil {
		proberSvc = probe.NewProbeService(cfg, nil, nil, probeLogger)
	}

	app := &AppModel{
		cfg:         cfg,
		theme:       th,
		keys:        keyMap,
		logger:      appLogger,
		discovery:   discoverer,
		prober:      proberSvc,
		header:      components.NewHeaderBar(th, cfg.Polling.Interval),
		tree:        components.NewSessionTreeView(th),
		inspect:     components.NewInspectPanel(th),
		footer:      components.NewFooterHelpBar(keyMap, th),
		toast:       components.NewInlineToast(th),
		modal:       components.NewModalLayer(th),
		spinner:     components.NewBrailleSpinner(cfg.Display.Animation),
		showInspect: true,
	}

	app.header.SetStats(components.FleetStats{})
	app.footer.SetContext(components.FooterContext{})
	return app
}

// Init starts animation and the first refresh cycle.
func (m *AppModel) Init() tea.Cmd {
	m.nextRefresh = time.Now().Add(m.cfg.Polling.Interval)
	m.header.SetRefreshDeadline(m.nextRefresh)
	m.logger.Info("app init",
		"refresh_interval", m.cfg.Polling.Interval,
		"host_include_patterns", len(m.cfg.Hosts.Include),
		"host_ignore_patterns", len(m.cfg.Hosts.Ignore),
		"theme", m.cfg.Display.Theme,
	)
	return tea.Batch(
		m.header.Init(),
		m.spinner.Init(),
		tickCmd(),
		m.refreshCmd(),
	)
}

// Given a message, when Update runs, then app and child component states are reconciled.
func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(typed.Width, typed.Height)

	case components.SpinnerTickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(typed)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case model.TickMsg:
		refreshDue := !m.nextRefresh.IsZero() && !typed.Now.Before(m.nextRefresh)
		m.logger.Debug("update message", "message_type", "TickMsg", "refresh_due", refreshDue)
		if refreshDue {
			cmds = append(cmds, m.refreshCmd())
		}
		m.header.SetRefreshDeadline(m.nextRefresh)
		cmds = append(cmds, tickCmd())

	case model.ProbeResultMsg:
		m.logger.Debug("update message", "message_type", "ProbeResultMsg", "hosts", len(typed.Hosts), "has_error", typed.Err != nil)
		if toastCmd := m.applyProbeResult(typed); toastCmd != nil {
			cmds = append(cmds, toastCmd)
		}

	case model.AttachFinishedMsg:
		if typed.Err != nil {
			m.logger.Info("attach finished", "status", "error")
			m.logger.Error("session attach failed", "error", sanitizeError(typed.Err))
		} else {
			m.logger.Info("attach finished", "status", "success")
		}
		if typed.Err != nil {
			if toastCmd := m.showErrorToast(typed.Err); toastCmd != nil {
				cmds = append(cmds, toastCmd)
			}
		}
		cmds = append(cmds, m.refreshCmd())

	case model.ModalConfirmCreateMsg:
		if host := m.findHostByName(typed.HostName); host != nil {
			cmds = append(cmds, m.createSessionCmd(*host, typed.Directory))
		}

	case model.ModalConfirmNewDirMsg:
		if host := m.findHostByName(typed.HostName); host != nil {
			cmds = append(cmds, m.createSessionCmd(*host, typed.Directory))
		}

	case model.ModalConfirmGitCloneMsg:
		if host := m.findHostByName(typed.HostName); host != nil {
			cmds = append(cmds, m.gitCloneSessionCmd(*host, typed.GitURL))
		}

	case model.ModalConfirmKillMsg:
		if host := m.findHostByName(typed.HostName); host != nil {
			cmds = append(cmds, m.killSessionCmd(*host, typed.SessionID, typed.Directory))
		}

	case model.CreateSessionFinishedMsg:
		if typed.Err != nil {
			m.logger.Info("create session finished", "status", "error")
			m.logger.Error("session create failed", "error", sanitizeError(typed.Err))
		} else {
			m.logger.Info("create session finished", "status", "success")
		}
		if typed.Err != nil {
			if toastCmd := m.showErrorToast(typed.Err); toastCmd != nil {
				cmds = append(cmds, toastCmd)
			}
		}
		cmds = append(cmds, m.refreshCmd())

	case model.KillSessionFinishedMsg:
		if typed.Err != nil {
			m.logger.Info("kill session finished", "status", "error")
			m.logger.Error("session kill failed", "error", sanitizeError(typed.Err))
		} else {
			m.logger.Info("kill session finished", "status", "success")
		}
		if typed.Err != nil {
			if toastCmd := m.showErrorToast(typed.Err); toastCmd != nil {
				cmds = append(cmds, toastCmd)
			}
		}
		cmds = append(cmds, m.refreshCmd())

	case model.GitCloneFinishedMsg:
		if typed.Err != nil {
			m.logger.Info("git clone finished", "status", "error")
			m.logger.Error("session git clone failed", "error", sanitizeError(typed.Err))
		} else {
			m.logger.Info("git clone finished", "status", "success")
		}
		if typed.Err != nil {
			if toastCmd := m.showErrorToast(typed.Err); toastCmd != nil {
				cmds = append(cmds, toastCmd)
			}
		}
		cmds = append(cmds, m.refreshCmd())

	case tea.KeyPressMsg:
		keyCategory := ""
		if m.modal.Active() {
			var modalCmd tea.Cmd
			m.modal, modalCmd = m.modal.Update(typed)
			if modalCmd != nil {
				cmds = append(cmds, modalCmd)
			}
			m.syncFooterContext()
			return m, tea.Batch(cmds...)
		}

		switch {
		case keys.Matches(typed.String(), m.keys.Quit):
			m.logger.Debug("update message", "message_type", "KeyPressMsg", "category", "quit")
			return m, tea.Quit
		case keys.Matches(typed.String(), m.keys.Refresh):
			keyCategory = "refresh"
			cmds = append(cmds, m.refreshCmd())
		case keys.Matches(typed.String(), m.keys.Search):
			keyCategory = "search"
			m.header.FocusSearch()
		case keys.Matches(typed.String(), m.keys.NewSession):
			keyCategory = "new_session"
			host, project, _, ok := m.tree.Selected()
			if ok && host != nil && host.Status == model.HostStatusOnline {
				if project != nil && len(project.Sessions) > 0 {
					m.modal.OpenNewSession(host.Name, project.Name, project.Sessions[0].Directory)
				} else {
					m.modal.OpenNewDirectory(host.Name)
				}
			}
		case keys.Matches(typed.String(), m.keys.KillSession):
			keyCategory = "kill_session"
			if host, _, session, ok := m.tree.Selected(); ok && host != nil && session != nil {
				m.modal.OpenConfirmKill(host.Name, session.ID, session.Directory)
			}
		case keys.Matches(typed.String(), m.keys.GitClone):
			keyCategory = "git_clone"
			host, _, _, ok := m.tree.Selected()
			if ok && host != nil && host.Status == model.HostStatusOnline {
				m.modal.OpenGitClone(host.Name)
			}
		case keys.Matches(typed.String(), m.keys.CycleView):
			keyCategory = "cycle_view"
			m.showInspect = !m.showInspect
			m.resize(m.width, m.height)
		case keys.Matches(typed.String(), m.keys.Inspect):
			keyCategory = "inspect"
			m.showInspect = true
			m.resize(m.width, m.height)
		case keys.Matches(typed.String(), m.keys.Attach):
			keyCategory = "attach"
			host, _, session, ok := m.tree.Selected()
			if ok && host != nil && session != nil {
				cmds = append(cmds, m.attachCmd(*host, *session))
			}
		case keys.Matches(typed.String(), m.keys.Authenticate):
			keyCategory = "authenticate"
			host, _, _, ok := m.tree.Selected()
			if ok && host != nil && (host.Status == model.HostStatusAuthRequired || host.Transport == model.TransportBlocked) {
				bootstrapCmds := m.getMultiHopBootstrapCmds(*host)
				if len(bootstrapCmds) > 0 {
					m.modal.OpenAuthBootstrap(host.Name, bootstrapCmds)
				}
			}
		case keys.Matches(typed.String(), m.keys.ErrorDetail):
			keyCategory = "error_detail"
			if m.toast.Visible() && m.lastError != nil {
				m.modal.OpenError(m.lastError)
			}
		}
		if keyCategory != "" {
			m.logger.Debug("update message", "message_type", "KeyPressMsg", "category", keyCategory)
		}
	}

	var cmd tea.Cmd
	m.header, cmd = m.header.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	m.tree, cmd = m.tree.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	toastWasVisible := m.toast.Visible()
	m.toast, cmd = m.toast.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if toastWasVisible != m.toast.Visible() {
		m.resize(m.width, m.height)
	}

	m.tree.SetFilter(m.header.SearchQuery())
	m.syncInspectSelection()
	m.syncFooterContext()

	return m, tea.Batch(cmds...)
}

// View renders the composed TUI.
func (m *AppModel) View() tea.View {
	header := m.header.View(time.Now(), m.theme.Spinner.Render(m.spinner.Frame()))
	left := m.tree.View()

	right := m.theme.Inspect.Render(m.theme.Muted.Render("Inspect panel hidden (press i/tab)"))
	if m.showInspect {
		right = m.inspect.View()
	}

	mainPane := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	toast := m.toast.View()
	footer := m.footer.View()

	sections := []string{header, mainPane}
	if toast != "" {
		sections = append(sections, toast)
	}
	sections = append(sections, footer)
	screen := lipgloss.JoinVertical(lipgloss.Left, sections...)

	if m.modal.Active() {
		screen = lipgloss.JoinVertical(lipgloss.Left, screen, m.modal.View())
	}

	v := tea.NewView(screen)
	v.AltScreen = true
	return v
}

func (m *AppModel) refreshCmd() tea.Cmd {
	discoverer := m.discovery
	proberSvc := m.prober
	timeout := m.cfg.Polling.Timeout

	return func() tea.Msg {
		startedAt := time.Now()
		m.logger.Info("refresh started", "timeout", timeout)

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		hosts, discoverErr := discoverer.Discover(ctx)
		probed, probeErr := proberSvc.ProbeHosts(ctx, hosts)

		resultErr := probeErr
		if discoverErr != nil {
			resultErr = errors.Join(discoverErr, probeErr)
		}

		elapsed := time.Since(startedAt)
		errCount := countRefreshErrors(probed, resultErr)
		m.logger.Info("refresh complete", "hosts", len(probed), "duration", elapsed, "errors", errCount)
		if resultErr != nil {
			m.logger.Error(
				"refresh failed",
				"error", sanitizeError(resultErr),
				"discover_error", discoverErr != nil,
				"probe_error", probeErr != nil,
			)
		}

		return model.ProbeResultMsg{
			Hosts:       probed,
			Err:         resultErr,
			RefreshedAt: time.Now(),
		}
	}
}

func (m *AppModel) applyProbeResult(msg model.ProbeResultMsg) tea.Cmd {
	hostsBefore := len(m.hosts)
	errorsBefore := countHostErrors(m.hosts)

	m.hosts = append([]model.Host(nil), msg.Hosts...)
	m.tree.SetHosts(m.hosts)
	stats := calculateFleetStats(m.hosts)
	m.header.SetStats(stats)

	refreshedAt := msg.RefreshedAt
	if refreshedAt.IsZero() {
		refreshedAt = time.Now()
	}
	m.nextRefresh = refreshedAt.Add(m.cfg.Polling.Interval)
	m.header.SetRefreshDeadline(m.nextRefresh)
	m.logger.Debug(
		"apply probe result",
		"hosts_before", hostsBefore,
		"hosts_after", len(m.hosts),
		"errors_before", errorsBefore,
		"errors_after", countHostErrors(m.hosts),
		"has_error", msg.Err != nil,
	)

	m.lastError = msg.Err
	if msg.Err != nil {
		// Don't open error modal for auth-required hosts; those are handled
		// via the dedicated auth bootstrap flow.
		hasNonAuthErrors := false
		for _, h := range msg.Hosts {
			if h.Status == model.HostStatusError || h.Status == model.HostStatusOffline {
				if h.LastError != "" {
					hasNonAuthErrors = true
					break
				}
			}
		}
		if hasNonAuthErrors {
			toastCmd := m.showErrorToast(msg.Err)
			m.syncInspectSelection()
			return toastCmd
		}
	}

	m.syncInspectSelection()
	return nil
}

func (m *AppModel) syncInspectSelection() {
	host, project, session, ok := m.tree.Selected()
	if !ok || session == nil || project == nil || host == nil {
		m.inspect.ClearSelection()
		return
	}
	m.inspect.SetSelection(*host, *project, *session)
}

func (m *AppModel) resize(width, height int) {
	m.width = width
	m.height = height

	m.header.SetSize(width)
	m.toast.SetSize(width)
	m.footer.SetSize(width)
	m.modal.SetSize(width, height)

	chromeHeight := 4
	if m.toast.Visible() {
		chromeHeight++
	}
	mainHeight := maxInt(1, height-chromeHeight)
	if !m.showInspect {
		m.tree.SetSize(width, mainHeight)
		m.inspect.SetSize(0, mainHeight)
		return
	}

	left := int(float64(width) * 0.58)
	right := width - left
	if right < 32 {
		right = 32
		left = maxInt(0, width-right)
	}

	m.tree.SetSize(left, mainHeight)
	m.inspect.SetSize(right, mainHeight)
}

func calculateFleetStats(hosts []model.Host) components.FleetStats {
	stats := components.FleetStats{HostsTotal: len(hosts)}
	for _, host := range hosts {
		switch host.Status {
		case model.HostStatusOnline:
			stats.HostsOnline++
		case model.HostStatusAuthRequired:
			// Count auth-required hosts separately; don't inflate online count.
		default:
			// offline, error, probing — no online increment.
		}
		stats.SessionsTotal += host.SessionCount()
	}
	return stats
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return model.TickMsg{Now: t}
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *AppModel) getMultiHopBootstrapCmds(host model.Host) []string {
	controlPath := m.cfg.SSH.ControlPath
	if controlPath == "" {
		controlPath = "~/.ssh/ocr-%n-%C"
	}
	persist := m.cfg.SSH.ControlPersist
	if persist <= 0 {
		persist = 600
	}
	timeout := m.cfg.SSH.ConnectTimeout
	if timeout <= 0 {
		timeout = 10
	}

	makeCmd := func(h model.Host) string {
		return fmt.Sprintf(
			"ssh -o ControlMaster=yes -o ControlPath=%s -o ControlPersist=%d -o ConnectTimeout=%d -Nf %s",
			controlPath, persist, timeout, h.Name,
		)
	}

	// Build alias index for dependency lookups
	aliasIndex := make(map[string]int, len(m.hosts))
	for i, h := range m.hosts {
		aliasIndex[h.Name] = i
	}

	var cmds []string

	// Generate commands for each jump hop that needs auth (in chain order)
	for _, hop := range host.JumpChain {
		if hop.External || hop.AliasRef == "" {
			continue
		}
		if idx, ok := aliasIndex[hop.AliasRef]; ok {
			jumpHost := m.hosts[idx]
			if jumpHost.Transport == model.TransportAuthRequired || jumpHost.Status == model.HostStatusAuthRequired {
				cmds = append(cmds, makeCmd(jumpHost))
			}
		}
	}

	// Then the target host itself if it needs auth
	if host.Status == model.HostStatusAuthRequired || host.Transport == model.TransportAuthRequired {
		cmds = append(cmds, makeCmd(host))
	}

	return cmds
}

func (m *AppModel) attachCmd(host model.Host, session model.Session) tea.Cmd {
	m.logger.Info("attach initiated", "host", host.Name, "project", session.Project, "session_id", session.ID)

	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	var remoteCmd string
	if session.Directory != "" {
		remoteCmd = fmt.Sprintf(
			`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); cd %s && exec "$OC" -s %s`,
			bin, bin, session.Directory, session.ID,
		)
	} else {
		remoteCmd = fmt.Sprintf(
			`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); exec "$OC" -s %s`,
			bin, bin, session.ID,
		)
	}

	c := exec.Command("ssh", "-t", host.Name, remoteCmd)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return model.AttachFinishedMsg{Err: err}
	})
}

func (m *AppModel) showErrorToast(err error) tea.Cmd {
	m.lastError = err
	if err == nil {
		return nil
	}
	m.logger.Error("error toast shown", "error", sanitizeError(err), "timeout", errorToastTimeout)

	cmd := m.toast.Show(err.Error(), components.ToastSeverityError, errorToastTimeout)
	m.resize(m.width, m.height)
	return cmd
}

func (m *AppModel) syncFooterContext() {
	m.footer.SetContext(components.FooterContext{
		ModalOpen:         m.modal.Active(),
		SearchFocus:       m.header.SearchFocused(),
		ErrorDetailActive: m.toast.Visible() && m.lastError != nil,
	})
}

func (m *AppModel) createSessionCmd(host model.Host, directory string) tea.Cmd {
	m.logger.Info("create session initiated", "host", host.Name, "directory", directory)

	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	remoteCmd := fmt.Sprintf(
		`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); cd %s && exec "$OC"`,
		bin, bin, directory,
	)

	c := exec.Command("ssh", "-t", host.Name, remoteCmd)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return model.CreateSessionFinishedMsg{Err: err}
	})
}

func (m *AppModel) killSessionCmd(host model.Host, sessionID, directory string) tea.Cmd {
	m.logger.Info("kill session initiated", "host", host.Name, "session_id", sessionID)

	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	remoteCmd := fmt.Sprintf(
		`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); cd %s && "$OC" session archive %s`,
		bin, bin, directory, sessionID,
	)

	return func() tea.Msg {
		c := exec.Command("ssh", host.Name, remoteCmd)
		err := c.Run()
		return model.KillSessionFinishedMsg{Err: err}
	}
}

func (m *AppModel) gitCloneSessionCmd(host model.Host, gitURL string) tea.Cmd {
	m.logger.Info("git clone initiated", "host", host.Name, "git_url", gitURL)

	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	repoDir := repoNameFromURL(gitURL)
	remoteCmd := fmt.Sprintf(
		`git clone %s %s && OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); cd %s && exec "$OC"`,
		gitURL, repoDir, bin, bin, repoDir,
	)

	c := exec.Command("ssh", "-t", host.Name, remoteCmd)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return model.GitCloneFinishedMsg{Err: err}
	})
}

func (m *AppModel) findHostByName(name string) *model.Host {
	for i := range m.hosts {
		if m.hosts[i].Name == name {
			return &m.hosts[i]
		}
	}
	return nil
}

func repoNameFromURL(gitURL string) string {
	base := path.Base(gitURL)
	return strings.TrimSuffix(base, ".git")
}

func countHostErrors(hosts []model.Host) int {
	count := 0
	for _, host := range hosts {
		if host.LastError != "" || host.TransportError != "" {
			count++
		}
	}
	return count
}

func countRefreshErrors(hosts []model.Host, refreshErr error) int {
	count := countHostErrors(hosts)
	if refreshErr != nil {
		count++
	}
	return count
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}

	msg := strings.TrimSpace(err.Error())
	msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	msg = strings.Join(strings.Fields(msg), " ")
	msg = redactCommandOutputTail(msg)

	runes := []rune(msg)
	if len(runes) > maxSanitizedErrorRunes {
		msg = strings.TrimSpace(string(runes[:maxSanitizedErrorRunes-1])) + "…"
	}

	return msg
}

func redactCommandOutputTail(msg string) string {
	if msg == "" {
		return ""
	}

	type marker struct {
		needle string
		label  string
	}

	markers := []marker{
		{needle: "stderr:", label: "stderr"},
		{needle: "stdout:", label: "stdout"},
	}

	lower := strings.ToLower(msg)
	firstIdx := -1
	firstLabel := ""
	for _, m := range markers {
		if idx := strings.Index(lower, m.needle); idx >= 0 && (firstIdx == -1 || idx < firstIdx) {
			firstIdx = idx
			firstLabel = m.label
		}
	}

	if firstIdx == -1 {
		return msg
	}

	prefix := strings.TrimSpace(msg[:firstIdx])
	if prefix == "" {
		return firstLabel + ": [redacted]"
	}

	return prefix + " " + firstLabel + ": [redacted]"
}
