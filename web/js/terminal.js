import { DOM } from './dom.js';
import { state } from './state.js';
import { loadChatHistory } from './chat.js';

let term = null;
export let fitAddon = null;
let ws = null;
let reconnectTimeout = null;
let reconnectDelay = 1000;

export async function attachTerminal(sessionId) {
  state.activeTerminalSessionId = sessionId;
  DOM.viewSessions.style.display = 'none';
  DOM.viewTerminal.style.display = 'flex';
  DOM.terminalSessionId.textContent = sessionId;
  if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = 'Loading history...';

  if (!term) {
    term = new window.Terminal({
      theme: {
        background: '#000000',
        foreground: '#e0e0e0',
        cursor: '#00ff41'
      },
      fontFamily: "'JetBrains Mono', monospace",
      fontSize: 14
    });
    fitAddon = new window.FitAddon.FitAddon();
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

export function connectTerminalWS(sessionId) {
  if (state.activeTerminalSessionId !== sessionId) return;

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
    reconnectDelay = 1000;
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
    if (state.activeTerminalSessionId !== sessionId) return;
    term.writeln(`\r\n\x1b[33m> Disconnected (code: ${e.code}). Reconnecting in ${reconnectDelay}ms...\x1b[0m`);
    if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = `Reconnecting in ${reconnectDelay}ms...`;

    if (reconnectTimeout) {
      clearTimeout(reconnectTimeout);
      reconnectTimeout = null;
    }
    
    reconnectTimeout = setTimeout(() => {
      connectTerminalWS(sessionId);
    }, reconnectDelay);
    
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  };

  ws.onerror = (e) => {
    term.writeln(`\r\n\x1b[31m> WebSocket error.\x1b[0m`);
    if (DOM.terminalConnectionStatus) DOM.terminalConnectionStatus.textContent = 'WebSocket error';
  };
}

export async function hydrateTerminalScrollback(sessionId) {
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

export function detachTerminal() {
  state.activeTerminalSessionId = null;
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

export function initTerminalUI() {
  DOM.btnDetachTerminal.addEventListener('click', detachTerminal);

  if (DOM.splitResizer) {
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
}