const vscode = acquireVsCodeApi();

const state = {
  session: null,
  messages: [],
  activeAssistantId: null,
  streaming: false
};

const dom = {
  title: document.getElementById('session-title'),
  streamState: document.getElementById('stream-state'),
  messages: document.getElementById('messages'),
  form: document.getElementById('chat-form'),
  input: document.getElementById('chat-input'),
  send: document.getElementById('chat-send')
};

const fileRefPattern = /(^|[\s(])((?:\.{0,2}\/|~\/)?[A-Za-z0-9_./-]+\.(?:ts|tsx|js|jsx|mjs|cjs|go|py|md|json|ya?ml|txt|css|scss|html|sql))(?:\:(\d+))?/g;

function makeId() {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function escapeHtml(value) {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function normalizeDiffMarkdown(input) {
  if (!input) {
    return '';
  }

  const lines = input.split('\n');
  let inCode = false;
  let inDiff = false;

  for (let i = 0; i < lines.length; i += 1) {
    if (lines[i].startsWith('```')) {
      inCode = !inCode;
      if (inDiff) {
        lines.splice(i, 0, '```');
        i += 1;
        inDiff = false;
      }
      continue;
    }

    if (inCode) {
      continue;
    }

    const isDiffLine = /^[+-] /.test(lines[i]);
    if (isDiffLine && !inDiff) {
      lines.splice(i, 0, '```diff');
      i += 1;
      inDiff = true;
      continue;
    }

    if (!isDiffLine && inDiff && lines[i].trim() !== '') {
      lines.splice(i, 0, '```');
      i += 1;
      inDiff = false;
    }
  }

  if (inDiff) {
    lines.push('```');
  }

  return lines.join('\n');
}

function linkifyFileRefs(text) {
  return text.replace(fileRefPattern, (full, prefix, filePath, line) => {
    const encodedPath = encodeURIComponent(filePath);
    const encodedLine = line ? encodeURIComponent(line) : '';
    const display = line ? `${filePath}:${line}` : filePath;
    return `${prefix}<a href="#" class="file-ref" data-file-path="${encodedPath}" data-file-line="${encodedLine}">${display}</a>`;
  });
}

function renderInline(text) {
  let rendered = escapeHtml(text);
  rendered = linkifyFileRefs(rendered);
  rendered = rendered.replace(/`([^`]+)`/g, (_m, value) => `<code>${value}</code>`);
  rendered = rendered.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  rendered = rendered.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  rendered = rendered.replace(/\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g, '<a href="$2">$1</a>');
  return rendered;
}

function looksLikeDiff(code) {
  return /^[-+]/m.test(code);
}

function renderDiffCode(code) {
  return escapeHtml(code)
    .split('\n')
    .map((line) => {
      if (line.startsWith('+')) {
        return `<span class="diff-add">${line}</span>`;
      }
      if (line.startsWith('-')) {
        return `<span class="diff-remove">${line}</span>`;
      }
      return line;
    })
    .join('\n');
}

function renderCodeBlock(language, code) {
  const normalizedLanguage = (language || '').toLowerCase();
  if (normalizedLanguage === 'diff' || looksLikeDiff(code)) {
    const encoded = encodeURIComponent(code);
    return `<div class="diff-preview"><div class="diff-preview-header"><span>Diff Preview</span><button type="button" class="apply-diff" data-diff="${encoded}">Apply</button></div><pre><code>${renderDiffCode(code)}</code></pre></div>`;
  }
  return `<pre><code>${escapeHtml(code)}</code></pre>`;
}

function renderMarkdown(markdown) {
  const normalized = normalizeDiffMarkdown(markdown || '');
  const codeBlocks = [];
  const tokenized = normalized.replace(/```([a-zA-Z0-9_-]+)?\n([\s\S]*?)```/g, (_full, language, code) => {
    const index = codeBlocks.push({ language: language || '', code }) - 1;
    return `@@CODE_BLOCK_${index}@@`;
  });

  const lines = tokenized.split('\n');
  let html = '';
  let inList = false;

  for (const line of lines) {
    const codeMatch = line.match(/^@@CODE_BLOCK_(\d+)@@$/);
    if (codeMatch) {
      if (inList) {
        html += '</ul>';
        inList = false;
      }
      const block = codeBlocks[Number(codeMatch[1])];
      html += renderCodeBlock(block.language, block.code);
      continue;
    }

    const trimmed = line.trim();
    if (!trimmed) {
      if (inList) {
        html += '</ul>';
        inList = false;
      }
      continue;
    }

    const heading = trimmed.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      if (inList) {
        html += '</ul>';
        inList = false;
      }
      const level = heading[1].length;
      html += `<h${level}>${renderInline(heading[2])}</h${level}>`;
      continue;
    }

    const listItem = trimmed.match(/^[-*]\s+(.*)$/);
    if (listItem) {
      if (!inList) {
        html += '<ul>';
        inList = true;
      }
      html += `<li>${renderInline(listItem[1])}</li>`;
      continue;
    }

    if (inList) {
      html += '</ul>';
      inList = false;
    }
    html += `<p>${renderInline(trimmed)}</p>`;
  }

  if (inList) {
    html += '</ul>';
  }

  return html;
}

function firstString(value, fallback = '') {
  if (typeof value === 'string' && value.trim()) {
    return value.trim();
  }
  return fallback;
}

function extractToolCall(chunk) {
  const type = firstString(chunk.type || '');
  const payload = chunk.payload && typeof chunk.payload === 'object' ? chunk.payload : null;
  if (!payload) {
    return null;
  }

  const payloadType = firstString(payload.type || payload.kind || '');
  const name = firstString(payload.name || payload.tool || payload.toolName || payload.call || 'tool');
  if (!type.toLowerCase().includes('tool') && !payloadType.toLowerCase().includes('tool') && !payload.input && !payload.arguments) {
    return null;
  }

  return {
    name,
    input: payload.input || payload.arguments || payload.args || payload.params || payload
  };
}

function renderToolCall(toolCall) {
  const details = document.createElement('details');
  details.className = 'tool-call';

  const summary = document.createElement('summary');
  summary.textContent = `Tool Call: ${firstString(toolCall.name, 'tool')}`;
  details.appendChild(summary);

  const pre = document.createElement('pre');
  pre.textContent = JSON.stringify(toolCall.input, null, 2);
  details.appendChild(pre);

  return details;
}

function renderMessageNode(message) {
  const container = document.createElement('section');
  container.className = `message ${message.role}`;

  const header = document.createElement('div');
  header.className = 'message-header';
  header.textContent = message.role.toUpperCase();
  container.appendChild(header);

  const body = document.createElement('div');
  body.className = 'message-body';
  if (message.role === 'assistant') {
    body.innerHTML = renderMarkdown(message.content || '');
  } else {
    body.textContent = message.content || '';
  }
  container.appendChild(body);

  if (message.toolCalls && message.toolCalls.length > 0) {
    const tools = document.createElement('div');
    tools.className = 'tool-calls';
    for (const toolCall of message.toolCalls) {
      tools.appendChild(renderToolCall(toolCall));
    }
    container.appendChild(tools);
  }

  wireInteractiveElements(container);
  return container;
}

function wireInteractiveElements(root) {
  const fileLinks = root.querySelectorAll('.file-ref');
  for (const link of fileLinks) {
    link.addEventListener('click', (event) => {
      event.preventDefault();
      const target = event.currentTarget;
      const path = decodeURIComponent(target.getAttribute('data-file-path') || '');
      const lineRaw = decodeURIComponent(target.getAttribute('data-file-line') || '');
      const line = Number.parseInt(lineRaw, 10);
      vscode.postMessage({
        type: 'openFile',
        path,
        line: Number.isFinite(line) ? line : undefined
      });
    });
  }

  const applyButtons = root.querySelectorAll('.apply-diff');
  for (const button of applyButtons) {
    button.addEventListener('click', (event) => {
      const target = event.currentTarget;
      const diff = decodeURIComponent(target.getAttribute('data-diff') || '');
      vscode.postMessage({ type: 'applyDiff', diff });
    });
  }
}

function renderMessages() {
  dom.messages.innerHTML = '';
  for (const message of state.messages) {
    dom.messages.appendChild(renderMessageNode(message));
  }
  dom.messages.scrollTop = dom.messages.scrollHeight;
}

function updateHeader() {
  if (!state.session) {
    dom.title.textContent = 'No session selected';
  } else {
    const description = state.session.workspacePath ? ` · ${state.session.workspacePath}` : '';
    dom.title.textContent = `${state.session.label || state.session.id}${description}`;
  }
  dom.streamState.textContent = state.streaming ? 'streaming' : 'idle';
  dom.send.disabled = !state.session || state.streaming;
}

function appendMessage(role, content) {
  const message = {
    id: makeId(),
    role,
    content,
    toolCalls: []
  };
  state.messages.push(message);
  renderMessages();
  return message;
}

function getMessageById(id) {
  return state.messages.find((message) => message.id === id) || null;
}

function ensureActiveAssistantMessage() {
  if (state.activeAssistantId) {
    const existing = getMessageById(state.activeAssistantId);
    if (existing) {
      return existing;
    }
  }

  const created = appendMessage('assistant', '');
  state.activeAssistantId = created.id;
  return created;
}

function handleChatChunk(chunk) {
  if (!chunk || typeof chunk !== 'object') {
    return;
  }

  if (chunk.error) {
    appendMessage('system', String(chunk.error));
    state.activeAssistantId = null;
    state.streaming = false;
    updateHeader();
    return;
  }

  const assistant = ensureActiveAssistantMessage();
  if (typeof chunk.delta === 'string' && chunk.delta.length > 0) {
    assistant.content += chunk.delta;
  }

  const toolCall = extractToolCall(chunk);
  if (toolCall) {
    assistant.toolCalls.push(toolCall);
  }

  if (chunk.done === true) {
    state.activeAssistantId = null;
    state.streaming = false;
  }

  renderMessages();
  updateHeader();
}

function replaceHistory(messages) {
  state.messages = [];
  if (Array.isArray(messages)) {
    for (const item of messages) {
      if (!item || typeof item !== 'object') {
        continue;
      }
      state.messages.push({
        id: firstString(item.id, makeId()),
        role: ['user', 'assistant', 'system'].includes(item.role) ? item.role : 'assistant',
        content: firstString(item.content, ''),
        toolCalls: Array.isArray(item.toolCalls) ? item.toolCalls : []
      });
    }
  }
  state.activeAssistantId = null;
  renderMessages();
}

dom.form.addEventListener('submit', (event) => {
  event.preventDefault();
  const prompt = dom.input.value.trim();
  if (!prompt || !state.session || state.streaming) {
    return;
  }

  appendMessage('user', prompt);
  const assistant = appendMessage('assistant', '');
  state.activeAssistantId = assistant.id;
  state.streaming = true;
  updateHeader();

  dom.input.value = '';
  vscode.postMessage({ type: 'sendPrompt', prompt });
});

window.addEventListener('message', (event) => {
  const msg = event.data;
  if (!msg || typeof msg !== 'object') {
    return;
  }

  switch (msg.type) {
    case 'session':
      state.session = msg.session || null;
      updateHeader();
      if (state.session) {
        vscode.postMessage({ type: 'requestHistory' });
      }
      break;
    case 'chatHistory':
      replaceHistory(msg.messages || []);
      break;
    case 'streamStarted':
      state.streaming = true;
      updateHeader();
      break;
    case 'streamEnded':
      state.streaming = false;
      state.activeAssistantId = null;
      updateHeader();
      break;
    case 'chatChunk':
      handleChatChunk(msg.chunk || {});
      break;
    case 'error':
      appendMessage('system', firstString(msg.message, 'Unknown error'));
      state.streaming = false;
      state.activeAssistantId = null;
      updateHeader();
      break;
    default:
      break;
  }
});

updateHeader();
vscode.postMessage({ type: 'ready' });
