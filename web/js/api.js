import { state } from './state.js';
import { DOM } from './dom.js';
import { render } from './ui.js';

export function normalizeSSEtoView(sseSession) {
  if (!sseSession) return null;
  return {
    id: sseSession.ID,
    workspacePath: sseSession.WorkspacePath,
    status: sseSession.Status,
    daemonPort: sseSession.DaemonPort,
    labels: sseSession.Labels || {}
  };
}

let evtSource = null;
let sseReconnectTimeout = null;

export function clearSSEReconnectTimer() {
  if (sseReconnectTimeout) {
    clearTimeout(sseReconnectTimeout);
    sseReconnectTimeout = null;
  }
}

export function setSSEIndicator(mode, detail) {
  if (!DOM.sseIndicator) return;
  if (mode === 'connected') {
    DOM.sseIndicator.textContent = '● ONLINE';
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

export function scheduleSSEReconnect(delayMs, reason) {
  if (evtSource || sseReconnectTimeout) return;
  setSSEIndicator('reconnecting', reason || 'retrying');
  sseReconnectTimeout = setTimeout(() => {
    sseReconnectTimeout = null;
    connectSSE();
  }, delayMs);
}

export function connectSSE() {
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

export async function loadInitial() {
  try {
    const res = await fetch('/api/sessions');
    if (!res.ok) throw new Error(`HTTP error! status: ${res.status}`);
    const data = await res.json();
    state.sessions.clear();
    (data || []).forEach(s => { state.sessions.set(s.id, s); });
    render();
    connectSSE();
  } catch (e) {
    console.error('Failed to load initial sessions', e);
    setSSEIndicator('disconnected', 'bootstrap failed');
    setTimeout(loadInitial, 5000);
  }
}
