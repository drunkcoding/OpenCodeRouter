import { DOM } from './dom.js';
import { state } from './state.js';
import { processDiffs } from './utils.js';

export async function loadChatHistory(sessionId) {
  if (DOM.chatHistory) DOM.chatHistory.innerHTML = '';
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

export function appendChatMessage(role, content) {
  const div = document.createElement('div');
  div.className = `chat-message ${role}`;
  
  if (role === 'assistant') {
    try {
      div.innerHTML = window.marked.parse(processDiffs(content || ''));
    } catch (e) {
      div.textContent = content;
    }
  } else {
    div.textContent = content;
  }
  
  if (DOM.chatHistory) {
    DOM.chatHistory.appendChild(div);
    DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
  }
  return div;
}

export function initChat() {
  if (DOM.chatForm) {
    DOM.chatForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (!state.activeTerminalSessionId) return;
      
      const prompt = DOM.chatInput.value.trim();
      if (!prompt) return;
      
      DOM.chatInput.value = '';
      DOM.btnSendChat.disabled = true;
      
      appendChatMessage('user', prompt);
      const assistantMsgDiv = appendChatMessage('assistant', '...');
      let currentResponse = '';

      try {
        const res = await fetch(`/api/sessions/${state.activeTerminalSessionId}/chat`, {
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
                      assistantMsgDiv.innerHTML = window.marked.parse(processDiffs(currentResponse));
                    } catch (err) {
                      assistantMsgDiv.textContent = currentResponse;
                    }
                    if (DOM.chatHistory) DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
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
                  if (DOM.chatHistory) DOM.chatHistory.scrollTop = DOM.chatHistory.scrollHeight;
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
  }

  if (DOM.chatInput) {
    DOM.chatInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        DOM.chatForm.dispatchEvent(new Event('submit'));
      }
    });
  }
}