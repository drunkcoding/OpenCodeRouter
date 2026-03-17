export const DOM = {
  sseIndicator: null,
  statOnline: null,
  statTotal: null,
  tbody: null,
  searchInput: null,
  emptyState: null,
  table: null,
  btnCreate: null,
  modal: null,
  btnCloseModal: null,
  formCreate: null,
  inputWorkspace: null,
  inputLabel: null,
  viewSessions: null,
  viewTerminal: null,
  terminalContainer: null,
  terminalSessionId: null,
  terminalConnectionStatus: null,
  btnDetachTerminal: null,
  chatHistory: null,
  chatForm: null,
  chatInput: null,
  chatContainer: null,
  splitResizer: null,
  btnSendChat: null
};

export function initDOM() {
  DOM.sseIndicator = document.getElementById('sse-indicator');
  DOM.statOnline = document.getElementById('stat-online');
  DOM.statTotal = document.getElementById('stat-total');
  DOM.tbody = document.getElementById('sessions-body');
  DOM.searchInput = document.getElementById('search-input');
  DOM.emptyState = document.getElementById('empty-state');
  DOM.table = document.getElementById('sessions-table');
  DOM.btnCreate = document.getElementById('btn-create-session');
  DOM.modal = document.getElementById('modal-overlay');
  DOM.btnCloseModal = document.getElementById('btn-close-modal');
  DOM.formCreate = document.getElementById('create-session-form');
  DOM.inputWorkspace = document.getElementById('input-workspace');
  DOM.inputLabel = document.getElementById('input-label');
  DOM.viewSessions = document.getElementById('view-sessions');
  DOM.viewTerminal = document.getElementById('view-terminal');
  DOM.terminalContainer = document.getElementById('terminal-container');
  DOM.terminalSessionId = document.getElementById('terminal-session-id');
  DOM.terminalConnectionStatus = document.getElementById('terminal-connection-status');
  DOM.btnDetachTerminal = document.getElementById('btn-detach-terminal');
  DOM.chatHistory = document.getElementById('chat-history');
  DOM.chatForm = document.getElementById('chat-form');
  DOM.chatInput = document.getElementById('chat-input');
  DOM.chatContainer = document.getElementById('chat-container');
  DOM.splitResizer = document.getElementById('split-resizer');
  DOM.btnSendChat = document.getElementById('btn-send-chat');
}