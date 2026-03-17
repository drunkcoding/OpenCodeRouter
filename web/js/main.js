import { initDOM } from './dom.js';
import { initMarked } from './utils.js';
import { initUI, render } from './ui.js';
import { initChat } from './chat.js';
import { initTerminalUI, attachTerminal } from './terminal.js';
import { loadInitial } from './api.js';
import { state } from './state.js';

document.addEventListener('DOMContentLoaded', () => {
  initDOM();
  initMarked();
  initUI();
  initChat();
  initTerminalUI();

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

  window.addEventListener('app:reloadSessions', () => {
    loadInitial();
  });

  loadInitial();
});