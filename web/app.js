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
    formCreate: document.getElementById('create-session-form'),
    inputWorkspace: document.getElementById('input-workspace'),
    inputLabel: document.getElementById('input-label'), // repurposed to label
    viewSessions: document.getElementById('view-sessions'),
    viewTerminal: document.getElementById('view-terminal'),
    terminalContainer: document.getElementById('terminal-container'),
    terminalSessionId: document.getElementById('terminal-session-id'),
    btnDetachTerminal: document.getElementById('btn-detach-terminal'),
    chatHistory: document.getElementById('chat-history'),
    chatForm: document.getElementById('chat-form'),
    chatInput: document.getElementById('chat-input'),
    splitResizer: document.getElementById('split-resizer'),
    btnSendChat: document.getElementById('btn-send-chat')
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
  
  function connectSSE() {
    if (evtSource) return;
    
    evtSource = new EventSource('/api/events');
    
    evtSource.onopen = () => {
      DOM.sseIndicator.textContent = '● STREAM_ACTIVE';
      DOM.sseIndicator.className = 'pulse-indicator online';
    };

    evtSource.onerror = () => {
      DOM.sseIndicator.textContent = '● RECONNECTING...';
      DOM.sseIndicator.className = 'pulse-indicator';
      evtSource.close();
      evtSource = null;
      setTimeout(connectSSE, 2000);
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

  function attachTerminal(sessionId) {
    activeTerminalSessionId = sessionId;
    DOM.viewSessions.style.display = 'none';
    DOM.viewTerminal.style.display = 'flex';
    DOM.terminalSessionId.textContent = sessionId;

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
    term.writeln(`\x1b[36m> Attaching to session ${sessionId}...\x1b[0m`);
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
      term.writeln(`\x1b[32m> Connection established.\x1b[0m`);
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
      
      reconnectTimeout = setTimeout(() => {
        connectTerminalWS(sessionId);
      }, reconnectDelay);
      
      // Exponential backoff capped at 30s
      reconnectDelay = Math.min(reconnectDelay * 2, 30000);
    };

    ws.onerror = (e) => {
      term.writeln(`\r\n\x1b[31m> WebSocket error.\x1b[0m`);
    };
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
  }

  DOM.btnDetachTerminal.addEventListener('click', detachTerminal);

  // Bootstrap
  loadInitial();

  // Chat Logic
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
        DOM.chatContainer.style.flex = '1 1 0%';
        if (terminalFitAddon) {
          terminalFitAddon.fit();
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
