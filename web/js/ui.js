import { state } from './state.js';
import { DOM } from './dom.js';

export function render() {
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

export function initUI() {
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
      
      // Dispatch custom event to trigger loadInitial without importing it
      window.dispatchEvent(new Event('app:reloadSessions'));
    } catch (err) {
      alert(`Network error creating session`);
    }
  });
}