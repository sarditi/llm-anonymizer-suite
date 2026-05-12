(function () {
  const vscode = acquireVsCodeApi();

  const state = {
    sessions: [],
    activeId: null,
    sessionSettingsErrors: [],
    settingsOpen: false,
    busy: false,
  };

  const $ = (sel) => document.querySelector(sel);

  function classifyLine(text) {
    if (/^=== .* ===\s*$/.test(text)) return 'heading';
    if (/^NOT_DO:\s*REASON:/.test(text)) return 'notdoreason';
    if (/^NOT_DO:/.test(text)) return 'notdo';
    if (/^REASON:/.test(text)) return 'reason';
    if (/^DIFF:/.test(text)) return 'diff';
    return null;
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function renderSidebar() {
    const list = $('#sessionList');
    list.innerHTML = '';
    for (const s of state.sessions) {
      const item = document.createElement('div');
      item.className = 'sessionItem' + (s.id === state.activeId ? ' active' : '');
      item.dataset.id = s.id;
      item.title = s.title;
      const label = document.createElement('span');
      label.textContent = s.title || 'New session';
      label.style.cssText = 'overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0';
      const del = document.createElement('button');
      del.className = 'delBtn';
      del.title = 'Delete session';
      del.textContent = '×';
      del.addEventListener('click', (e) => {
        e.stopPropagation();
        vscode.postMessage({ type: 'deleteSession', id: s.id });
      });
      item.addEventListener('click', () => {
        vscode.postMessage({ type: 'selectSession', id: s.id });
      });
      item.appendChild(label);
      item.appendChild(del);
      list.appendChild(item);
    }
  }

  function renderStatus() {
    const bar = $('#statusBar');
    bar.classList.remove('error');
    const errs = state.sessionSettingsErrors || [];
    if (!state.activeId) {
      bar.innerHTML = '<span style="color:var(--muted)">No active session</span>';
      return;
    }
    if (errs.length > 0) {
      bar.classList.add('error');
      const summary = errs.length === 1
        ? escapeHtml(errs[0])
        : escapeHtml(errs[0]) + ` (+${errs.length - 1} more)`;
      bar.innerHTML =
        `<span>⚠ ${summary}</span>` +
        `<span><a id="openSettingsLink">Configure</a></span>`;
    } else {
      bar.innerHTML =
        `<span>✓ Session configured</span>` +
        `<span><a id="openSettingsLink">Settings</a></span>`;
    }
    const link = $('#openSettingsLink');
    if (link) {
      link.addEventListener('click', () => {
        state.settingsOpen = !state.settingsOpen;
        renderSettingsPanel();
      });
    }
  }

  function renderSettingsPanel() {
    const panel = $('#settingsPanel');
    const session = state.sessions.find((s) => s.id === state.activeId);
    if (!session) {
      panel.innerHTML = '';
      return;
    }
    const s = session.settings || { codebaseDirs: [], uploadDir: '', downloadDir: '' };
    const open = state.settingsOpen;

    let html = `<div class="sp-header" id="spToggle">
      <span>⚙ Session Settings</span>
      <span>${open ? '▲' : '▼'}</span>
    </div>`;

    if (open) {
      html += `<div class="sp-body">
        <div class="sp-row">
          <span class="sp-label">Upload dir</span>
          <span class="sp-value" title="${escapeHtml(s.uploadDir || '')}">${escapeHtml(s.uploadDir || '(not set)')}</span>
          <button class="sp-btn" data-action="pickDir" data-field="uploadDir">Browse</button>
        </div>
        <div class="sp-row">
          <span class="sp-label">Download dir</span>
          <span class="sp-value" title="${escapeHtml(s.downloadDir || '')}">${escapeHtml(s.downloadDir || '(not set)')}</span>
          <button class="sp-btn" data-action="pickDir" data-field="downloadDir">Browse</button>
        </div>
        <div class="sp-row sp-dirs-header">
          <span class="sp-label">Codebase dirs</span>
          <button class="sp-btn" data-action="addCodebaseDir">+ Add</button>
        </div>`;

      if (s.codebaseDirs.length === 0) {
        html += `<div class="sp-dir-empty">No directories added</div>`;
      } else {
        for (const dir of s.codebaseDirs) {
          html += `<div class="sp-dir-item">
            <span class="sp-dir-path" title="${escapeHtml(dir)}">${escapeHtml(dir)}</span>
            <button class="sp-btn sp-btn-danger" data-action="removeCodebaseDir" data-dir="${escapeHtml(dir)}">×</button>
          </div>`;
        }
      }
      html += `</div>`;
    }

    panel.innerHTML = html;

    $('#spToggle').addEventListener('click', () => {
      state.settingsOpen = !state.settingsOpen;
      renderSettingsPanel();
    });

    panel.querySelectorAll('[data-action]').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const action = btn.dataset.action;
        if (action === 'pickDir') {
          vscode.postMessage({ type: 'pickSessionDir', field: btn.dataset.field });
        } else if (action === 'addCodebaseDir') {
          vscode.postMessage({ type: 'addSessionCodebaseDir' });
        } else if (action === 'removeCodebaseDir') {
          vscode.postMessage({ type: 'removeSessionCodebaseDir', dir: btn.dataset.dir });
        }
      });
    });
  }

  function renderTurns() {
    const wrap = $('#turns');
    wrap.innerHTML = '';
    const session = state.sessions.find((s) => s.id === state.activeId);
    if (!session) {
      wrap.innerHTML = '<div class="banner info">Create or select a session to begin.</div>';
      return;
    }
    for (const t of session.turns) {
      const card = document.createElement('div');
      card.className = 'turn';

      const prompt = document.createElement('div');
      prompt.className = 'promptBlock';
      prompt.textContent = t.promptText;
      card.appendChild(prompt);

      const meta = document.createElement('div');
      meta.className = 'meta';
      const parts = [`status: ${t.status}`];
      if (t.scriptName) parts.push(`script: ${t.scriptName}`);
      if (t.promptFilePath) parts.push(`prompt: ${t.promptFilePath}`);
      meta.textContent = parts.join('  ·  ');
      card.appendChild(meta);

      const out = document.createElement('div');
      out.className = 'output';
      for (const line of t.output) {
        const span = document.createElement('span');
        span.className = 'line';
        if (line.stream === 'stderr') span.classList.add('stderr');
        else if (line.stream === 'system') span.classList.add('system');
        else {
          const cls = classifyLine(line.text);
          if (cls) span.classList.add(cls);
        }
        span.textContent = line.text || ' ';
        out.appendChild(span);
      }
      card.appendChild(out);
      wrap.appendChild(card);
    }
    wrap.scrollTop = wrap.scrollHeight;
  }

  function syncComposer() {
    const session = state.sessions.find((s) => s.id === state.activeId);
    const sendBtn = $('#sendBtn');
    const cancelBtn = $('#cancelBtn');
    const rollbackBtn = $('#rollbackBtn');
    const ta = $('#promptInput');

    const hasErrors = (state.sessionSettingsErrors || []).length > 0;
    const lastTurn = session && session.turns.length > 0
      ? session.turns[session.turns.length - 1]
      : null;
    const inFlight = lastTurn &&
      (lastTurn.status === 'pending' ||
       lastTurn.status === 'awaiting-script' ||
       lastTurn.status === 'running');

    sendBtn.disabled = !session || hasErrors || !!inFlight;
    cancelBtn.disabled = !inFlight;
    rollbackBtn.disabled = !lastTurn || !lastTurn.rollbackAvailable || !!inFlight;
    ta.disabled = !session || hasErrors || !!inFlight;
  }

  function send() {
    const ta = $('#promptInput');
    const text = ta.value.trim();
    if (!text) return;
    vscode.postMessage({ type: 'sendPrompt', text });
    ta.value = '';
  }

  function init() {
    $('#newSessionBtn').addEventListener('click', () => {
      vscode.postMessage({ type: 'newSession' });
    });
    $('#sendBtn').addEventListener('click', send);
    $('#cancelBtn').addEventListener('click', () => {
      vscode.postMessage({ type: 'cancelTurn' });
    });
    $('#rollbackBtn').addEventListener('click', () => {
      vscode.postMessage({ type: 'rollback' });
    });
    $('#promptInput').addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        send();
      }
    });

    window.addEventListener('message', (event) => {
      const msg = event.data;
      if (msg.type === 'state') {
        state.sessions = msg.sessions || [];
        state.activeId = msg.activeId || null;
        state.sessionSettingsErrors = msg.sessionSettingsErrors || [];
        renderSidebar();
        renderStatus();
        renderSettingsPanel();
        renderTurns();
        syncComposer();
      } else if (msg.type === 'appendOutput') {
        const session = state.sessions.find((s) => s.id === msg.sessionId);
        if (!session) return;
        const turn = session.turns.find((t) => t.id === msg.turnId);
        if (!turn) return;
        turn.output.push({ text: msg.text, stream: msg.stream });
        renderTurns();
      } else if (msg.type === 'turnUpdate') {
        const session = state.sessions.find((s) => s.id === msg.sessionId);
        if (!session) return;
        const turn = session.turns.find((t) => t.id === msg.turn.id);
        if (turn) Object.assign(turn, msg.turn);
        else session.turns.push(msg.turn);
        renderTurns();
        syncComposer();
      }
    });

    vscode.postMessage({ type: 'ready' });
  }

  init();
})();
