package tui

import (
	"context"
	"errors"
	"fmt"
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
	cfg   config.Config
	theme theme.Theme
	keys  keys.KeyMap

	discovery Discoverer
	prober    Prober

	header  components.HeaderBar
	tree    components.SessionTreeView
	inspect components.InspectPanel
	footer  components.FooterHelpBar
	modal   components.ModalLayer
	spinner components.BrailleSpinner

	hosts       []model.Host
	lastError   error
	nextRefresh time.Time
	width       int
	height      int
	showInspect bool
}

// NewApp constructs the root model with injected services.
func NewApp(cfg config.Config, discoverer Discoverer, proberSvc Prober) *AppModel {
	th := theme.ByName(cfg.Display.Theme)
	keyMap := keys.NewKeyMap(cfg.Keybindings)

	if discoverer == nil {
		discoverer = discovery.NewDiscoveryService(cfg, nil)
	}
	if proberSvc == nil {
		cache := probe.NewCacheStore(cfg.Cache.TTL)
		proberSvc = probe.NewProbeService(cfg, nil, cache)
	}

	app := &AppModel{
		cfg:         cfg,
		theme:       th,
		keys:        keyMap,
		discovery:   discoverer,
		prober:      proberSvc,
		header:      components.NewHeaderBar(th, cfg.Polling.Interval),
		tree:        components.NewSessionTreeView(th),
		inspect:     components.NewInspectPanel(th),
		footer:      components.NewFooterHelpBar(keyMap, th),
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
		if !m.nextRefresh.IsZero() && !typed.Now.Before(m.nextRefresh) {
			cmds = append(cmds, m.refreshCmd())
		}
		m.header.SetRefreshDeadline(m.nextRefresh)
		cmds = append(cmds, tickCmd())

	case model.ProbeResultMsg:
		m.applyProbeResult(typed)

	case tea.KeyPressMsg:
		if m.modal.Active() {
			var modalCmd tea.Cmd
			m.modal, modalCmd = m.modal.Update(typed)
			if modalCmd != nil {
				cmds = append(cmds, modalCmd)
			}
			m.footer.SetContext(components.FooterContext{ModalOpen: m.modal.Active(), SearchFocus: m.header.SearchFocused()})
			return m, tea.Batch(cmds...)
		}

		switch {
		case keys.Matches(typed.String(), m.keys.Quit):
			return m, tea.Quit
		case keys.Matches(typed.String(), m.keys.Refresh):
			cmds = append(cmds, m.refreshCmd())
		case keys.Matches(typed.String(), m.keys.Search):
			m.header.FocusSearch()
		case keys.Matches(typed.String(), m.keys.NewSession):
			m.modal.OpenNewSession()
		case keys.Matches(typed.String(), m.keys.KillSession):
			if _, _, session, ok := m.tree.Selected(); ok && session != nil {
				m.modal.OpenConfirmKill(session.ID)
			}
		case keys.Matches(typed.String(), m.keys.CycleView):
			m.showInspect = !m.showInspect
			m.resize(m.width, m.height)
		case keys.Matches(typed.String(), m.keys.Inspect):
			m.showInspect = true
			m.resize(m.width, m.height)
		case keys.Matches(typed.String(), m.keys.Attach):
			if _, _, session, ok := m.tree.Selected(); ok && session != nil {
				m.modal.OpenError(fmt.Errorf("attach action not wired for session %s", session.ID))
			}
		case keys.Matches(typed.String(), m.keys.Authenticate):
			host, _, _, ok := m.tree.Selected()
			if ok && host != nil && (host.Status == model.HostStatusAuthRequired || host.Transport == model.TransportBlocked) {
				bootstrapCmds := m.getMultiHopBootstrapCmds(*host)
				if len(bootstrapCmds) > 0 {
					m.modal.OpenAuthBootstrap(host.Name, bootstrapCmds)
				}
			}
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

	m.tree.SetFilter(m.header.SearchQuery())
	m.syncInspectSelection()
	m.footer.SetContext(components.FooterContext{ModalOpen: m.modal.Active(), SearchFocus: m.header.SearchFocused()})

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
	footer := m.footer.View()
	screen := lipgloss.JoinVertical(lipgloss.Left, header, mainPane, footer)

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
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		hosts, discoverErr := discoverer.Discover(ctx)
		probed, probeErr := proberSvc.ProbeHosts(ctx, hosts)

		resultErr := probeErr
		if discoverErr != nil {
			resultErr = errors.Join(discoverErr, probeErr)
		}

		return model.ProbeResultMsg{
			Hosts:       probed,
			Err:         resultErr,
			RefreshedAt: time.Now(),
		}
	}
}

func (m *AppModel) applyProbeResult(msg model.ProbeResultMsg) {
	m.hosts = append([]model.Host(nil), msg.Hosts...)
	m.tree.SetHosts(m.hosts)
	m.header.SetStats(calculateFleetStats(m.hosts))

	refreshedAt := msg.RefreshedAt
	if refreshedAt.IsZero() {
		refreshedAt = time.Now()
	}
	m.nextRefresh = refreshedAt.Add(m.cfg.Polling.Interval)
	m.header.SetRefreshDeadline(m.nextRefresh)

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
			m.modal.OpenError(msg.Err)
		}
	}

	m.syncInspectSelection()
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
	m.footer.SetSize(width)
	m.modal.SetSize(width, height)

	mainHeight := maxInt(1, height-4)
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
