document.addEventListener('DOMContentLoaded', () => {

if (typeof marked !== 'undefined') {
  const renderer = new marked.Renderer();
  const originalCode = renderer.code.bind(renderer);
  renderer.code = function(token) {
    const text = typeof token === 'string' ? token : token.text;
    const lang = typeof token === 'string' ? arguments[1] : token.lang;
    
    if (lang === 'diff' || (!lang && (text.match(/^-[^-]/m) || text.match(/^\+[^+]/m)))) {
      const lines = text.split('\n').map(line => {
        if (line.startsWith('+')) return '<span class="diff-add">' + line.replace(/</g, '&lt;').replace(/>/g, '&gt;') + '</span>';
        if (line.startsWith('-')) return '<span class="diff-rm">' + line.replace(/</g, '&lt;').replace(/>/g, '&gt;') + '</span>';
        return line.replace(/</g, '&lt;').replace(/>/g, '&gt;');
      });
      return '<pre class="diff-block" style="background:#1e1e1e;padding:10px;border-radius:4px;overflow-x:auto;"><code>' + lines.join('\n') + '</code></pre>';
    }
    return originalCode.apply(this, arguments);
  };
  marked.use({ renderer });
}

function processDiffs(text) {
  if (!text) return '';
  const lines = text.split('\n');
  let inDiff = false;
  let inCodeBlock = false;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].startsWith('```')) {
      inCodeBlock = !inCodeBlock;
      if (inDiff) {
        lines.splice(i, 0, '```');
        inDiff = false;
        i++;
      }
      continue;
    }
    if (!inCodeBlock) {
      const isDiffLine = lines[i].match(/^[+-] /) && lines[i].length > 2;
      if (isDiffLine && !inDiff) {
        lines.splice(i, 0, '```diff');
        inDiff = true;
        i++;
      } else if (!isDiffLine && inDiff && lines[i].trim() !== '') {
        lines.splice(i, 0, '```');
        inDiff = false;
        i++;
      }
    }
  }
  if (inDiff) lines.push('```');
  return lines.join('\n');
}

  const state = {
    sessions: new Map(),
    filter: '',
    sortCol: 'id',
    sortDesc: false
  };

  const DOM = {
    sseIndicator: document.getElementById('sse-indicator'),
    statOnline: document.getElementById('stat-online'),
    statTotal: document.getElementById('stat-total'),
    tbody: document.getElementById('sessions-body'),
    searchInput: document.getElementById('search-input'),
    emptyState: document.getElementById('empty-state'),
    table: document.getElementById('sessions-table'),
    btnCreate: document.getElementById('btn-create-session'),
    modal: document.getElementById('modal-overlay'),
    btnCloseModal: document.getElementById('btn-close-modal'),
    authModalOverlay: document.getElementById('auth-modal-overlay'),
    btnCloseAuthModal: document.getElementById('btn-close-auth-modal'),
    authForm: document.getElementById('auth-form'),
    inputPassword: document.getElementById('input-password'),
    authHostLabel: document.getElementById('auth-host-label'),
    authAgentStatus: document.getElementById('auth-agent-status'),
    formCreate: document.getElementById('create-session-form'),
    inputWorkspace: document.getElementById('input-workspace'),
    inputLabel: document.getElementById('input-label'), // repurposed to label
    viewSessions: document.getElementById('view-sessions'),
    viewTerminal: document.getElementById('view-terminal'),
    terminalContainer: document.getElementById('terminal-container'),
    terminalSessionId: document.getElementById('terminal-session-id'),
    terminalConnectionStatus: document.getElementById('terminal-connection-status'),
    btnDetachTerminal: document.getElementById('btn-detach-terminal'),
    chatHistory: document.getElementById('chat-history'),
    chatForm: document.getElementById('chat-form'),
    chatInput: document.getElementById('chat-input'),
    chatContainer: document.getElementById('chat-container'),
    splitResizer: document.getElementById('split-resizer'),
    btnSendChat: document.getElementById('btn-send-chat'),
    tabLocal: document.getElementById('tab-local'),
    tabRemote: document.getElementById('tab-remote'),
    viewRemote: document.getElementById('view-remote'),
    remoteHostsContainer: document.getElementById('remote-hosts-container'),
    remoteEmptyState: document.getElementById('remote-empty-state'),
    remoteErrorState: document.getElementById('remote-error-state'),
    btnRefreshRemote: document.getElementById('btn-refresh-remote'),
    remoteSearchInput: document.getElementById('remote-search-input')
  };

  function normalizeSSEtoView(sseSession) {
    if (!sseSession) return null;
    return {
      id: sseSession.ID,
      workspacePath: sseSession.WorkspacePath,
      status: sseSession.Status,
      daemonPort: sseSession.DaemonPort,
      labels: sseSession.Labels || {}
    };
  }

  // Setup EventSource
  let evtSource = null;
  let sseReconnectTimeout = null;

  function clearSSEReconnectTimer() {
    if (sseReconnectTimeout) {
      clearTimeout(sseReconnectTimeout);
      sseReconnectTimeout = null;
    }
  }

  function setSSEIndicator(mode, detail) {
    if (!DOM.sseIndicator) return;
    if (mode === 'connected') {
      DOM.sseIndicator.textContent = '● STREAM_ACTIVE';
      DOM.sseIndicator.className = 'pulse-indicator online';
      return;
    }
    if (mode === 'reconnecting') {
      DOM.sseIndicator.textContent = `● RECONNECTING${detail ? ` (${detail})` : '...'}`;
      DOM.sseIndicator.className = 'pulse-indicator';
      return;
    }
    DOM.sseIndicator.textContent = `● DISCONNECTED${detail ? ` (${detail})` : ''}`;
    DOM.sseIndicator.className = 'pulse-indicator';
  }

  function scheduleSSEReconnect(delayMs, reason) {
    if (evtSource || sseReconnectTimeout) return;
    setSSEIndicator('reconnecting', reason || 'retrying');
    sseReconnectTimeout = setTimeout(() => {
      sseReconnectTimeout = null;
      connectSSE();
    }, delayMs);
  }
  
  function connectSSE() {
    if (evtSource) return;
    clearSSEReconnectTimer();
    
    evtSource = new EventSource('/api/events');
    
    evtSource.onopen = () => {
      clearSSEReconnectTimer();
      setSSEIndicator('connected');
    };

    evtSource.onerror = () => {
      if (!evtSource) return;

      if (evtSource.readyState === EventSource.CONNECTING) {
        setSSEIndicator('reconnecting', 'auto');
        return;
      }

      const fatal = evtSource.readyState === EventSource.CLOSED;
      setSSEIndicator(fatal ? 'disconnected' : 'reconnecting', fatal ? 'closed' : 'retrying');
      evtSource.close();
      evtSource = null;
      scheduleSSEReconnect(2000, fatal ? 'closed' : 'retrying');
    };

    const handleSessionEvent = (e) => {
      try {
        const envelope = JSON.parse(e.data);
        if (!envelope.payload || !envelope.payload.Session) return;
        const norm = normalizeSSEtoView(envelope.payload.Session);
        
        // Preserve health if it exists
        if (state.sessions.has(norm.id)) {
          const existing = state.sessions.get(norm.id);
          norm.health = existing.health;
        }
        
        state.sessions.set(norm.id, norm);
        render();
      } catch (err) {
        console.error('Failed parsing SSE', err);
      }
    };

    evtSource.addEventListener('session.created', handleSessionEvent);
    evtSource.addEventListener('session.stopped', handleSessionEvent);
    evtSource.addEventListener('session.attached', handleSessionEvent);
    evtSource.addEventListener('session.detached', handleSessionEvent);
    
    evtSource.addEventListener('session.health', (e) => {
      try {
        const envelope = JSON.parse(e.data);
        if (!envelope.payload || !envelope.payload.Session) return;
        const norm = normalizeSSEtoView(envelope.payload.Session);
        
        // Apply health
        if (envelope.payload.Current) {
           norm.health = envelope.payload.Current;
        }

        state.sessions.set(norm.id, norm);
        render();
      } catch (err) {
        console.error('Failed parsing health SSE', err);
      }
    });

    }

  async function loadInitial() {
    try {
      const res = await fetch('/api/sessions');
      if (!res.ok) throw new Error(`HTTP error! status: ${res.status}`);
      const data = await res.json();
      state.sessions.clear();
      (data || []).forEach(s => state.sessions.set(s.id, s));
      render();
      connectSSE();
    } catch (e) {
      console.error('Failed to load initial sessions', e);
      setSSEIndicator('disconnected', 'bootstrap failed');
      setTimeout(loadInitial, 5000);
    }
  }

  function render() {
    const filterText = state.filter.toLowerCase();
    let total = 0;
    let online = 0;
    
    DOM.tbody.innerHTML = '';
    
    const sorted = Array.from(state.sessions.values()).sort((a, b) => {
      let valA = a[state.sortCol] || '';
      let valB = b[state.sortCol] || '';
      if (state.sortCol === 'label') {
        valA = a.labels?.label || a.labels?.name || '';
        valB = b.labels?.label || b.labels?.name || '';
      }
      const res = String(valA).localeCompare(String(valB));
      return state.sortDesc ? -res : res;
    });
    
    let visibleCount = 0;

    sorted.forEach(s => {
      total++;
      if (s.status === 'active' || s.status === 'idle') online++;
      
      const lbl = (s.labels && (s.labels.label || s.labels.name)) || '-';
      const searchable = `${s.id} ${lbl} ${s.workspacePath}`.toLowerCase();
      if (filterText && !searchable.includes(filterText)) return;
      
      visibleCount++;
      const tr = document.createElement('tr');
      
      let statusClass = 'error';
      if (s.status === 'active') statusClass = 'active';
      if (s.status === 'idle') statusClass = 'idle';
      if (s.status === 'stopped') statusClass = 'stopped';

      tr.innerHTML = `
        <td><span class="status-badge ${statusClass}">${s.status ? s.status.toUpperCase() : 'UNKNOWN'}</span></td>
        <td class="id-col">${s.id}</td>
        <td>${lbl}</td>
        <td><span class="workspace-col truncate" title="${s.workspacePath}">${s.workspacePath}</span></td>
        <td>
          <div class="action-group">
            <button class="cyber-button secondary" onclick="appAction('attach', '${s.id}')">ATTACH</button>
            ${(s.status === 'active' || s.status === 'idle') 
              ? `<button class="cyber-button warning" onclick="appAction('stop', '${s.id}')">STOP</button>`
              : `<button class="cyber-button" onclick="appAction('restart', '${s.id}')">START</button>`
            }
            <button class="cyber-button danger" onclick="appAction('delete', '${s.id}')">DEL</button>
          </div>
        </td>
      `;
      DOM.tbody.appendChild(tr);
    });

    DOM.statTotal.textContent = total;
    DOM.statOnline.textContent = online;
    
    if (visibleCount === 0) {
      DOM.table.style.display = 'none';
      DOM.emptyState.style.display = 'block';
    } else {
      DOM.table.style.display = 'table';
      DOM.emptyState.style.display = 'none';
    }
  }

  window.appAction = async (action, id) => {
    try {
      if (action === 'attach') {
        attachTerminal(id);
        return;
      }
      
      const method = action === 'delete' ? 'DELETE' : 'POST';
      const url = action === 'delete' ? `/api/sessions/${id}` : `/api/sessions/${id}/${action}`;
      
      const res = await fetch(url, { method });
      if (!res.ok) {
        const err = await res.json().catch(() => ({error: 'Unknown error'}));
        alert(`Action failed: ${err.error || res.statusText}`);
      } else {
        if (action === 'delete') {
           state.sessions.delete(id);
           render();
        }
      }
    } catch (e) {
      console.error(e);
      alert(`Network error during action: ${action}`);
    }
  };

  DOM.searchInput.addEventListener('input', (e) => {
    state.filter = e.target.value;
    render();
  });

  document.querySelectorAll('.cyber-table th').forEach(th => {
    if (th.textContent.includes('ACTIONS')) return;
    th.style.cursor = 'pointer';
    th.title = 'Click to sort';
    th.addEventListener('click', () => {
      const col = th.textContent.trim().toLowerCase();
      if (state.sortCol === col) {
        state.sortDesc = !state.sortDesc;
      } else {
        state.sortCol = col;
        state.sortDesc = false;
      }
      render();
    });
  });

  // Modal handlers
  DOM.btnCreate.addEventListener('click', () => {
    DOM.modal.style.display = 'flex';
    DOM.inputWorkspace.focus();
  });

  DOM.btnCloseModal.addEventListener('click', () => {
    DOM.modal.style.display = 'none';
  });

  DOM.formCreate.addEventListener('submit', async (e) => {
    e.preventDefault();
    const payload = {
      workspacePath: DOM.inputWorkspace.value,
      label: DOM.inputLabel.value || undefined
    };
    
    try {
      const res = await fetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      if (!res.ok) {
         const err = await res.json().catch(() => ({error: 'Unknown error'}));
         alert(`Create failed: ${err.error || res.statusText}`);
         return;
      }
      DOM.modal.style.display = 'none';
      DOM.formCreate.reset();
      
      // Usually handled by SSE, but if we get a response, we can fetch all or append.
      // fetch is safer to get the normalized view format.
      loadInitial();
    } catch (err) {
      alert(`Network error creating session`);
    }
  });


  // Terminal Logic
  let term = null;
  let fitAddon = null;
  let ws = null;
  let activeTerminalSessionId = null;
  let reconnectTimeout = null;
  let reconnectDelay = 1000;

  async function attachTerminal(sessionId) {
    activeTerminalSessionId = sessionId;
    DOM.viewSessions.style.display = 'none';
    DOM.viewTerminal.style.display = 'flex';
    DOM.terminalSessionId.textContent = sessionId;
    if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = 'Loading history...';

    if (!term) {
      term = new Terminal({
        theme: {
          background: '#000000',
          foreground: '#e0e0e0',
          cursor: '#00ff41'
        },
        fontFamily: "'JetBrains Mono', monospace",
        fontSize: 14
      });
      fitAddon = new FitAddon.FitAddon();
      term.loadAddon(fitAddon);
      term.open(DOM.terminalContainer);
      
      term.onData(data => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(data));
        }
      });

      term.onResize(size => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', cols: size.cols, rows: size.rows }));
        }
      });
      
      window.addEventListener('resize', () => {
        if (DOM.viewTerminal.style.display === 'flex') {
          fitAddon.fit();
        }
      });
    }

    term.clear();
    term.writeln(`\x1b[36m> Loading history...\x1b[0m`);
    await hydrateTerminalScrollback(sessionId);
    loadChatHistory(sessionId);
    
    reconnectDelay = 1000;
    setTimeout(() => {
      fitAddon.fit();
      connectTerminalWS(sessionId);
    }, 50);
  }

  function connectTerminalWS(sessionId) {
    if (activeTerminalSessionId !== sessionId) return;

    if (ws) {
      ws.close();
      ws = null;
    }
    if (reconnectTimeout) {
      clearTimeout(reconnectTimeout);
      reconnectTimeout = null;
    }

    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${location.host}/ws/terminal/${sessionId}`);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      term.writeln(`\x1b[32m> Connected\x1b[0m`);
      if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = 'Connected';
      reconnectDelay = 1000; // Reset backoff on success
      if (term.cols && term.rows) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    };

    ws.onmessage = (evt) => {
      if (evt.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(evt.data));
      } else if (evt.data instanceof Blob) {
        const reader = new FileReader();
        reader.onload = () => {
          term.write(new Uint8Array(reader.result));
        };
        reader.readAsArrayBuffer(evt.data);
      } else {
        term.write(evt.data);
      }
    };

    ws.onclose = (e) => {
      if (activeTerminalSessionId !== sessionId) return;
      term.writeln(`\r\n\x1b[33m> Disconnected (code: ${e.code}). Reconnecting in ${reconnectDelay}ms...\x1b[0m`);
      if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = `Reconnecting in ${reconnectDelay}ms...`;

      if (reconnectTimeout) {
        clearTimeout(reconnectTimeout);
        reconnectTimeout = null;
      }
      
      reconnectTimeout = setTimeout(() => {
        connectTerminalWS(sessionId);
      }, reconnectDelay);
      
      // Exponential backoff capped at 30s
      reconnectDelay = Math.min(reconnectDelay * 2, 30000);
    };

    ws.onerror = (e) => {
      term.writeln(`\r\n\x1b[31m> WebSocket error.\x1b[0m`);
      if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = 'WebSocket error';
    };
  }

  async function hydrateTerminalScrollback(sessionId) {
    try {
      const res = await fetch(`/api/sessions/${sessionId}/scrollback?type=terminal_output&limit=1000`);
      if (!res.ok) {
        term.writeln(`\x1b[33m> History unavailable (${res.status})\x1b[0m`);
        return;
      }
      const entries = await res.json();
      if (!Array.isArray(entries) || entries.length === 0) {
        return;
      }

      for (const entry of entries) {
        if (!entry || entry.type !== 'terminal_output') continue;
        const content = typeof entry.content === 'string' ? atob(entry.content) : '';
        if (!content) continue;
        term.write(content);
      }
    } catch (error) {
      term.writeln(`\x1b[33m> Failed to load history\x1b[0m`);
    }
  }

  function detachTerminal() {
    activeTerminalSessionId = null;
    if (reconnectTimeout) {
      clearTimeout(reconnectTimeout);
      reconnectTimeout = null;
    }
    if (ws) {
      ws.close();
      ws = null;
    }
    DOM.viewTerminal.style.display = 'none';
    DOM.viewSessions.style.display = 'block';
    DOM.terminalSessionId.textContent = '';
    if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = '';
  }

  DOM.btnDetachTerminal.addEventListener('click', detachTerminal);

  // Bootstrap
  loadInitial();

  const remoteState = {
    hosts: [],
    filter: '',
    isLoading: false,
    error: null,
    tunnels: [],
    expandedProjects: new Set()
  };

  let remoteRefreshTimer = null;

  function startRemoteRefreshTimer() {
    stopRemoteRefreshTimer();
    remoteRefreshTimer = setInterval(() => {
      fetchRemoteHosts(true);
    }, 30000);
  }

  function stopRemoteRefreshTimer() {
    if (remoteRefreshTimer) {
      clearInterval(remoteRefreshTimer);
      remoteRefreshTimer = null;
    }
  }

  window.switchTab = function(tabName) {
    if (tabName === 'local') {
      if(DOM.tabLocal) DOM.tabLocal.classList.add('tab-active');
      if(DOM.tabRemote) DOM.tabRemote.classList.remove('tab-active');
      if(DOM.viewSessions) DOM.viewSessions.style.display = 'block';
      if(DOM.viewRemote) DOM.viewRemote.style.display = 'none';
      if(DOM.viewTerminal) DOM.viewTerminal.style.display = 'none';
      stopRemoteRefreshTimer();
    } else if (tabName === 'remote') {
      if(DOM.tabRemote) DOM.tabRemote.classList.add('tab-active');
      if(DOM.tabLocal) DOM.tabLocal.classList.remove('tab-active');
      if(DOM.viewRemote) DOM.viewRemote.style.display = 'block';
      if(DOM.viewSessions) DOM.viewSessions.style.display = 'none';
      if(DOM.viewTerminal) DOM.viewTerminal.style.display = 'none';
      
      if (remoteState.hosts.length === 0 && !remoteState.isLoading) {
        fetchRemoteHosts();
      }
      startRemoteRefreshTimer();
    }
  };

  if(DOM.tabLocal) DOM.tabLocal.addEventListener('click', () => switchTab('local'));
  if(DOM.tabRemote) DOM.tabRemote.addEventListener('click', () => switchTab('remote'));

  async function fetchRemoteHosts(isSilentRefresh = false) {
    if (remoteState.isLoading) return;
    remoteState.isLoading = true;
    
    if (!isSilentRefresh && DOM.remoteEmptyState) {
      DOM.remoteEmptyState.style.display = 'block';
      if(DOM.remoteHostsContainer) DOM.remoteHostsContainer.innerHTML = '';
      if(DOM.remoteErrorState) DOM.remoteErrorState.style.display = 'none';
    }

    try {
      const qs = isSilentRefresh ? '?refresh=true' : '';
      const [res, tunnelsRes] = await Promise.all([
        fetch('/api/remote/hosts' + qs),
        fetch('/api/remote/tunnels')
      ]);
      
      if (!res.ok) throw new Error(`HTTP error! status: ${res.status}`);
      const data = await res.json();
      // The API returns { hosts: [...], warnings: [...] } so we handle both cases
      remoteState.hosts = data.hosts || data || [];
      remoteState.warnings = data.warnings || [];
      remoteState.error = null;
      
      if (tunnelsRes.ok) {
        remoteState.tunnels = await tunnelsRes.json();
        remoteState.serves = {};
        await Promise.all(remoteState.tunnels.filter(t => t.state === 'connected').map(async (t) => {
          try {
             const sr = await fetch(`/api/remote/serve/${t.hostAlias}`);
             if (sr.ok) {
                remoteState.serves[t.hostAlias] = await sr.json();
             }
          } catch(e) {}
        }));
      } else {
        remoteState.tunnels = [];
        remoteState.serves = {};
      }
    } catch (e) {
      console.error('Failed to fetch remote hosts', e);
      remoteState.error = e.message;
      if (!isSilentRefresh && DOM.remoteErrorState) {
        DOM.remoteErrorState.style.display = 'block';
        if(DOM.remoteEmptyState) DOM.remoteEmptyState.style.display = 'none';
      }
    } finally {
      remoteState.isLoading = false;
      renderRemoteHosts();
    }
  }

  window.toggleProjectExpanded = function(projectId) {
    if (remoteState.expandedProjects.has(projectId)) {
      remoteState.expandedProjects.delete(projectId);
    } else {
      remoteState.expandedProjects.add(projectId);
    }
    const el = document.getElementById(projectId + '-sessions');
    const toggleEl = document.getElementById(projectId + '-toggle');
    if (el) {
      if (remoteState.expandedProjects.has(projectId)) {
        el.style.display = 'block';
        if (toggleEl) toggleEl.textContent = '[-]';
      } else {
        el.style.display = 'none';
        if (toggleEl) toggleEl.textContent = '[+]';
      }
    }
  };


window.connectHost = async function(hostName) {
  try {
    const res = await fetch('/api/remote/tunnel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host: hostName })
    });
    
    if (res.status === 401 || res.status === 403 || (!res.ok && (await res.clone().json().catch(()=>({}))).code === 'AUTH_REQUIRED')) {
      showAuthModal(hostName);
      return;
    }
    if (!res.ok) {
      const err = await res.json().catch(()=>({message: 'Unknown error'}));
      if (err.code === 'AUTH_REQUIRED') {
        showAuthModal(hostName);
        return;
      }
      throw new Error(err.message || 'Failed to connect');
    }
    
    startFastRefresh();
  } catch (e) {
    alert('Connect failed: ' + e.message);
  }
};

window.disconnectHost = async function(tunnelId) {
  try {
    const res = await fetch(`/api/remote/tunnel/${tunnelId}`, { method: 'DELETE' });
    if (!res.ok) throw new Error('Failed to disconnect');
    fetchRemoteHosts(true);
  } catch (e) {
    alert('Disconnect failed: ' + e.message);
  }
};

window.startServe = async function(hostName, projectDir) {
  try {
    const res = await fetch('/api/remote/serve', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host: hostName, projectDir: projectDir })
    });
    if (!res.ok) {
      const err = await res.json().catch(()=>({message: 'Unknown error'}));
      throw new Error(err.message || 'Failed to start serve');
    }
    fetchRemoteHosts(true);
  } catch (e) {
    alert('Failed to start serve: ' + e.message);
  }
};

window.openOpencode = function(hostName, projectDir) {
  const encProject = encodeURIComponent(projectDir);
  window.open(`/remote/${hostName}/${encProject}/`, '_blank');
};

let fastRefreshTimer = null;
function startFastRefresh() {
  if (fastRefreshTimer) clearInterval(fastRefreshTimer);
  fetchRemoteHosts(true);
  fastRefreshTimer = setInterval(() => {
    const isConnecting = (remoteState.tunnels || []).some(t => t.state === 'connecting');
    if (!isConnecting) {
      clearInterval(fastRefreshTimer);
      fastRefreshTimer = null;
    }
    fetchRemoteHosts(true);
  }, 5000);
}

let currentAuthHost = '';

window.showAuthModal = async function(hostName) {
  currentAuthHost = hostName;
  if(DOM.authHostLabel) DOM.authHostLabel.innerText = `> PASSWORD FOR ${hostName}`;
  if(DOM.inputPassword) DOM.inputPassword.value = '';
  if(DOM.authModalOverlay) DOM.authModalOverlay.style.display = 'flex';
  if(DOM.inputPassword) DOM.inputPassword.focus();
  
  if(DOM.authAgentStatus) {
    DOM.authAgentStatus.innerText = 'Checking SSH agent...';
    try {
      const res = await fetch('/api/remote/auth/agent');
      if (res.ok) {
        const data = await res.json();
        if (data.available) {
          DOM.authAgentStatus.innerText = `SSH Agent active (Socket: ${data.socketPath})`;
        } else {
          DOM.authAgentStatus.innerText = 'SSH Agent not available or no identities.';
        }
      } else {
        DOM.authAgentStatus.innerText = 'SSH Agent status unknown.';
      }
    } catch(e) {
      DOM.authAgentStatus.innerText = 'Error checking SSH agent.';
    }
  }
};

if(DOM.btnCloseAuthModal) {
  DOM.btnCloseAuthModal.addEventListener('click', () => {
    if(DOM.authModalOverlay) DOM.authModalOverlay.style.display = 'none';
  });
}

if(DOM.authForm) {
  DOM.authForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const pwd = DOM.inputPassword.value;
    const btn = document.getElementById('btn-auth-submit');
    if(btn) btn.disabled = true;
    
    try {
      const res = await fetch('/api/remote/auth', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host: currentAuthHost, password: pwd })
      });
      
      if (!res.ok) {
        const err = await res.json().catch(()=>({message: 'Auth failed'}));
        throw new Error(err.message || 'Auth failed');
      }
      
      if(DOM.authModalOverlay) DOM.authModalOverlay.style.display = 'none';
      
      window.connectHost(currentAuthHost);
      
    } catch (err) {
      alert(err.message);
    } finally {
      if(btn) btn.disabled = false;
      if(DOM.inputPassword) DOM.inputPassword.value = '';
    }
  });
}

  function renderRemoteHosts() {
    if (!DOM.remoteHostsContainer) return;
    
    const filterText = remoteState.filter.toLowerCase();
    
    const visibleHosts = remoteState.hosts.filter(h => {
      if (!filterText) return true;
      const searchable = `${h.name} ${h.address} ${h.user}`.toLowerCase();
      if (searchable.includes(filterText)) return true;
      
      if (h.projects) {
        for (const p of h.projects) {
          if (p.name.toLowerCase().includes(filterText)) return true;
          if (p.path.toLowerCase().includes(filterText)) return true;
        }
      }
      return false;
    });

    if (remoteState.hosts.length === 0 && !remoteState.error) {
      DOM.remoteHostsContainer.innerHTML = '<div class="empty-state">> NO_REMOTE_HOSTS_FOUND</div>';
      if(DOM.remoteEmptyState) DOM.remoteEmptyState.style.display = 'none';
      return;
    }

    if (visibleHosts.length === 0 && remoteState.hosts.length > 0 && !remoteState.error) {
      DOM.remoteHostsContainer.innerHTML = '<div class="empty-state">> NO_MATCHING_HOSTS</div>';
      if(DOM.remoteEmptyState) DOM.remoteEmptyState.style.display = 'none';
      return;
    }

    if (remoteState.hosts.length > 0 && !remoteState.error) {
      if(DOM.remoteEmptyState) DOM.remoteEmptyState.style.display = 'none';
      if(DOM.remoteErrorState) DOM.remoteErrorState.style.display = 'none';
      
      let html = '';
      visibleHosts.forEach((host, hIdx) => {
        let statusClass = host.status === 'online' ? 'active' : 'error';
        let statusText = host.status.toUpperCase();
        if (host.status === 'auth_required') {
          statusClass = 'warning';
          statusText = 'AUTH_REQ';
        }
        
        const tunnel = (remoteState.tunnels || []).find(t => t.hostAlias === host.name || t.remoteHost === host.address || t.remoteHost === host.name);

        let projectsHtml = '';
        if (host.projects && host.projects.length > 0) {
          projectsHtml = `
            <div class="host-projects">
              ${host.projects.map((p, idx) => {
                const sessionCount = p.sessions ? p.sessions.length : 0;
                const projectId = ('project-' + host.name + '-' + idx).replace(/\W/g, '-');
                const isExpanded = remoteState.expandedProjects.has(projectId);
                
                let sessionsHtml = '';
                if (sessionCount > 0) {
                  sessionsHtml = `
                    <div class="project-sessions-list" id="${projectId}-sessions" style="display: ${isExpanded ? 'block' : 'none'}; padding-top: 0.5rem; margin-top: 0.5rem; border-top: 1px dashed rgba(51, 51, 51, 0.5);">
                      ${p.sessions.map(s => {
                        const sStatus = (s.status || 'unknown').toLowerCase();
                        let sStatusClass = 'error';
                        if (sStatus === 'active') sStatusClass = 'active';
                        else if (sStatus === 'idle') sStatusClass = 'idle';
                        else if (sStatus === 'stopped') sStatusClass = 'stopped';
                        
                        return `
                          <div class="project-session-item" style="display: flex; justify-content: space-between; align-items: center; padding: 0.25rem 0; font-size: 0.8rem;">
                            <div style="display: flex; flex-direction: column; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 70%;">
                              <span style="color: var(--fg-base);">${s.id}</span>
                              ${s.directory ? `<span style="color: var(--fg-muted); font-size: 0.7rem;" title="${s.directory}">${s.directory}</span>` : ''}
                            </div>
                            <span class="status-badge ${sStatusClass}" style="transform: scale(0.8); transform-origin: right;">${sStatus.toUpperCase()}</span>
                          </div>
                        `;
                      }).join('')}
                    </div>
                  `;
                }
                
                return `
                  <div class="host-project-container">
                    <div class="host-project-item" style="cursor: ${sessionCount > 0 ? 'pointer' : 'default'};" ${sessionCount > 0 ? `onclick="toggleProjectExpanded('${projectId}')"` : ''}>
                      <span class="project-name">${p.name}</span>
                      <span class="project-sessions" style="display: flex; align-items: center; gap: 0.5rem;">
                        ${sessionCount} session(s)
                        ${tunnel && tunnel.state === 'connected' ? `<button class="cyber-button secondary" style="font-size: 0.6rem; padding: 0.1rem 0.3rem;" onclick="event.stopPropagation(); window.openOpencode('${host.name}', '${p.path}')">OPEN IDE</button>` : ''}
                        ${sessionCount > 0 ? `<span id="${projectId}-toggle" style="font-size: 0.7rem; color: var(--accent-secondary);">${isExpanded ? '[-]' : '[+]'}</span>` : ''}
                      </span>
                    </div>
                    ${sessionsHtml}
                  </div>
                `;
              }).join('')}
            </div>
          `;
        }

        let actionBtn = '';
        let openCodeBtn = '';
        let tunnelStatusBadge = '';
        
        const serveStatus = (remoteState.serves || {})[host.name] || (remoteState.serves || {})[host.address];
        
        if (tunnel) {
            if (tunnel.state === 'connected') {
                actionBtn = `<button class="cyber-button error" style="margin-top: 0.5rem;" onclick="window.disconnectHost('${tunnel.id}')">[DISCONNECT]</button>`;
                tunnelStatusBadge = `<span class="status-badge active" style="margin-left: 0.5rem;">TUN_CONN</span>`;
                if (serveStatus && serveStatus.state === 'running') {
                    openCodeBtn = `<span class="status-badge active" style="margin-top: 0.5rem; margin-left: 0.5rem;">SERVE_RUNNING</span>`;
                } else if (serveStatus && serveStatus.state === 'starting') {
                    openCodeBtn = `<span class="status-badge idle" style="margin-top: 0.5rem; margin-left: 0.5rem;">SERVE_STARTING</span>`;
                } else {
                    openCodeBtn = `<button class="cyber-button primary" style="margin-top: 0.5rem; margin-left: 0.5rem;" onclick="window.startServe('${host.name}', '~')">[START_SERVE]</button>`;
                }
            } else if (tunnel.state === 'connecting') {
                actionBtn = `<button class="cyber-button secondary" disabled style="margin-top: 0.5rem;">[CONNECTING...]</button>
                             <button class="cyber-button error" style="margin-top: 0.5rem; margin-left: 0.5rem;" onclick="window.disconnectHost('${tunnel.id}')">[CANCEL]</button>`;
                tunnelStatusBadge = `<span class="status-badge idle" style="margin-left: 0.5rem;">TUN_WAIT</span>`;
            } else {
                actionBtn = `<button class="cyber-button secondary" style="margin-top: 0.5rem;" onclick="window.connectHost('${host.name}')">[CONNECT]</button>`;
                if (tunnel.state === 'error') {
                    tunnelStatusBadge = `<span class="status-badge error" style="margin-left: 0.5rem;" title="${tunnel.error}">TUN_ERR</span>`;
                }
            }
        } else {
            actionBtn = `<button class="cyber-button secondary" style="margin-top: 0.5rem;" onclick="window.connectHost('${host.name}')">[CONNECT]</button>`;
            if (host.status === 'auth_required') {
                actionBtn = `
                <button class="cyber-button secondary" disabled style="margin-top: 0.5rem;">[CONNECT]</button>
                <button class="cyber-button warning" style="margin-top: 0.5rem; margin-left: 0.5rem;" onclick="window.showAuthModal('${host.name}')">[AUTH]</button>
                `;
            }
        }

        html += `
          <div class="host-card remote-host-card">
            <div class="host-header">
              <div class="host-title">
                <span class="host-label">${host.label || host.name}</span>
                <span class="host-name">${host.user}@${host.address}</span>
              </div>
              <div style="display: flex; align-items: center;">
                <span class="status-badge ${statusClass}">${statusText}</span>
                ${tunnelStatusBadge}
              </div>
            </div>
            <div class="host-details">
              <div class="host-detail-row">
                <span class="host-detail-label">OpenCode</span>
                <span class="host-detail-value">${host.opencode_version || 'Not detected'}</span>
              </div>
              <div class="host-detail-row">
                <span class="host-detail-label">Latency</span>
                <span class="host-detail-value">${host.latency_ms > 0 ? host.latency_ms + 'ms' : '-'}</span>
              </div>
              <div class="host-detail-row">
                <span class="host-detail-label">SESSIONS</span>
                <span class="host-detail-value">${host.sessionCount || 0}</span>
              </div>
              ${tunnel && tunnel.error ? `<div class="host-detail-row" style="color: var(--accent-error); font-size: 0.8rem; margin-top: 0.5rem; grid-column: 1 / -1;">> ${tunnel.error}</div>` : ''}
            </div>
            <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
              ${actionBtn}
              ${openCodeBtn}
            </div>
            ${projectsHtml}
          </div>
        `;
      });
      DOM.remoteHostsContainer.innerHTML = html;
    }
  }

  if (DOM.remoteSearchInput) {
    DOM.remoteSearchInput.addEventListener('input', (e) => {
      remoteState.filter = e.target.value;
      renderRemoteHosts();
    });
  }

  if (DOM.btnRefreshRemote) {
    DOM.btnRefreshRemote.addEventListener('click', () => {
      fetchRemoteHosts(true);
    });
  }

  async function loadChatHistory(sessionId) {
    if(DOM.chatHistory) DOM.chatHistory.innerHTML = '';
    try {
      const res = await fetch(`/api/sessions/${sessionId}/chat`);
      if (!res.ok) return;
      const messages = await res.json();
      if (!messages || !Array.isArray(messages)) return;
      
      messages.forEach(msg => {
        let content = '';
        if (msg.parts && Array.isArray(msg.parts)) {
            content = msg.parts.map(p => p.text || '').join('');
        } else if (msg.content) {
            content = msg.content;
        }
        
        if (msg.role === 'user') {
          appendChatMessage('user', content);
        } else if (msg.role === 'assistant') {
          appendChatMessage('assistant', content);
        }
      });
    } catch (err) {
      console.error('Failed to load chat history', err);
    }
  }

  function appendChatMessage(role, content) {
    const div = document.createElement('div');
    div.className = `chat-message ${role}`;
    
    if (role === 'assistant') {
      try {
        div.innerHTML = marked.parse(processDiffs(content || ''));
      } catch (e) {
        div.textContent = content;
      }
    } else {
      div.textContent = content;
    }
    
    if(DOM.chatHistory) DOM.chatHistory.appendChild(div);
    if(DOM.chatHistory) DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
    return div;
  }

  if(DOM.chatForm) DOM.chatForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!activeTerminalSessionId) return;
    
    const prompt = DOM.chatInput.value.trim();
    if (!prompt) return;
    
    DOM.chatInput.value = '';
    DOM.btnSendChat.disabled = true;
    
    appendChatMessage('user', prompt);
    const assistantMsgDiv = appendChatMessage('assistant', '...');
    let currentResponse = '';

    try {
      const res = await fetch(`/api/sessions/${activeTerminalSessionId}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt })
      });
      
      if (!res.ok) {
        throw new Error(`HTTP error! status: ${res.status}`);
      }

      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop(); // keep the incomplete line in buffer
        
        for (const line of lines) {
          if (line.startsWith('data: ')) {
            try {
              const chunk = JSON.parse(line.slice(6));
              
              if (chunk.type === 'message.part.delta' || chunk.type === 'message.final') {
                if (chunk.delta) {
                  currentResponse += chunk.delta;
                  if (currentResponse === '...') currentResponse = chunk.delta; // clear initial dot
                  try {
                    assistantMsgDiv.innerHTML = marked.parse(processDiffs(currentResponse));
                  } catch (err) {
                    assistantMsgDiv.textContent = currentResponse;
                  }
                  DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
                }
              } else if (chunk.type === 'tool_call' || chunk.type === 'agent.tool_call') {
                // Render tool call
                const toolCall = document.createElement('div');
                toolCall.className = 'chat-tool-call';
                toolCall.innerHTML = `
                  <div class="chat-tool-header">
                    <span>> TOOL: ${chunk.payload?.name || 'unknown'}</span>
                    <span>[+]</span>
                  </div>
                  <div class="chat-tool-body">${JSON.stringify(chunk.payload?.input || {}, null, 2)}</div>
                `;
                toolCall.querySelector('.chat-tool-header').addEventListener('click', () => {
                  toolCall.classList.toggle('expanded');
                });
                assistantMsgDiv.appendChild(toolCall);
                DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
              }
            } catch (err) {
              console.error('Failed to parse SSE line', line, err);
            }
          }
        }
      }
    } catch (err) {
      console.error('Chat error', err);
      appendChatMessage('system', `Error: ${err.message}`);
    } finally {
      DOM.btnSendChat.disabled = false;
      DOM.chatInput.focus();
    }
  });

  
  // Split pane resizer logic
  if(DOM.splitResizer) {
    let isResizing = false;
    DOM.splitResizer.addEventListener('mousedown', (e) => {
      isResizing = true;
      DOM.splitResizer.classList.add('resizing');
      document.body.style.cursor = 'col-resize';
    });
    document.addEventListener('mousemove', (e) => {
      if (!isResizing) return;
      const splitView = DOM.terminalContainer.parentElement.getBoundingClientRect();
      const newWidth = e.clientX - splitView.left;
      const percentage = (newWidth / splitView.width) * 100;
      if (percentage > 10 && percentage < 90) {
        DOM.terminalContainer.style.flex = '0 0 ' + percentage + '%';
        if (DOM.chatContainer) {
          DOM.chatContainer.style.flex = '1 1 0%';
        }
        if (fitAddon) {
          fitAddon.fit();
        }
      }
    });
    document.addEventListener('mouseup', () => {
      if (isResizing) {
        isResizing = false;
        DOM.splitResizer.classList.remove('resizing');
        document.body.style.cursor = '';
      }
    });
  }

  if(DOM.chatInput) DOM.chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      DOM.chatForm.dispatchEvent(new Event('submit'));
    }
  });

});
