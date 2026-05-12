import * as vscode from 'vscode';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { SessionStore, SessionSettings, Turn } from './sessions';
import { readSettings, resolveSessionSettings, pickDirectory } from './settings';
import { buildScriptName } from './tfidf';
import { scanCodebases } from './codebaseScanner';
import { buildPrompt } from './promptBuilder';
import { runScript, makeExecutable, RunHandle } from './scriptRunner';

interface ActiveJob {
  sessionId: string;
  turnId: string;
  runner?: RunHandle;
  cancelPrompt?: () => void;
  cancelled: boolean;
}

interface PickItem extends vscode.QuickPickItem {
  value: string;
}

export class SidebarProvider implements vscode.WebviewViewProvider {
  public static readonly viewType = 'poorMansClaude.main';

  private view?: vscode.WebviewView;
  private activeSessionId: string | null = null;
  private activeJob: ActiveJob | null = null;

  constructor(
    private readonly ctx: vscode.ExtensionContext,
    private readonly store: SessionStore,
  ) {}

  public resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = {
      enableScripts: true,
      localResourceRoots: [vscode.Uri.joinPath(this.ctx.extensionUri, 'media')],
    };
    view.webview.html = this.renderHtml(view.webview);
    view.webview.onDidReceiveMessage((msg) => this.onMessage(msg));

    const cfgListener = vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('poorMansClaude')) this.pushState();
    });
    view.onDidDispose(() => cfgListener.dispose());
  }

  public refresh(): void {
    this.pushState();
  }

  private async onMessage(msg: any): Promise<void> {
    switch (msg.type) {
      case 'ready':
        if (!this.activeSessionId) {
          const sessions = this.store.list();
          this.activeSessionId = sessions[0]?.id ?? null;
        }
        this.pushState();
        break;

      case 'newSession': {
        const currentSettings = this.activeSessionId
          ? this.store.get(this.activeSessionId)?.settings
          : undefined;
        const globalSettings = readSettings();
        const initialSettings: SessionSettings = currentSettings ?? {
          codebaseDirs: globalSettings.codebaseDirs,
          uploadDir: globalSettings.uploadDir,
          downloadDir: globalSettings.downloadDir,
        };
        const s = await this.store.createSession(initialSettings);
        this.activeSessionId = s.id;
        this.pushState();
        break;
      }

      case 'selectSession':
        this.activeSessionId = msg.id;
        this.pushState();
        break;

      case 'deleteSession': {
        const toDelete = this.store.get(msg.id);
        const title = toDelete?.title || 'this session';
        const answer = await vscode.window.showWarningMessage(
          `Delete "${title}"?`,
          { modal: true },
          'Delete',
        );
        if (answer !== 'Delete') break;
        await this.store.deleteSession(msg.id);
        if (this.activeSessionId === msg.id) {
          const remaining = this.store.list();
          this.activeSessionId = remaining[0]?.id ?? null;
        }
        this.pushState();
        break;
      }

      case 'sendPrompt':
        await this.handleSend(msg.text);
        break;

      case 'cancelTurn':
        await this.cancelActive('user cancelled');
        break;

      case 'rollback':
        await this.handleRollback();
        break;

      case 'pickSessionDir': {
        if (!this.activeSessionId) break;
        const field = msg.field as 'uploadDir' | 'downloadDir';
        const label = field === 'uploadDir' ? 'Select Upload Directory' : 'Select Download Directory';
        const abs = await pickDirectory(label, true);
        if (!abs) break;
        const session = this.store.get(this.activeSessionId);
        if (!session) break;
        await this.store.updateSessionSettings(this.activeSessionId, { ...session.settings, [field]: abs });
        this.pushState();
        break;
      }

      case 'addSessionCodebaseDir': {
        if (!this.activeSessionId) break;
        const abs = await pickDirectory('Add Codebase Directory', false);
        if (!abs) break;
        const session = this.store.get(this.activeSessionId);
        if (!session) break;
        const dirs = session.settings.codebaseDirs;
        if (!dirs.includes(abs)) {
          await this.store.updateSessionSettings(this.activeSessionId, {
            ...session.settings,
            codebaseDirs: [...dirs, abs],
          });
        }
        this.pushState();
        break;
      }

      case 'removeSessionCodebaseDir': {
        if (!this.activeSessionId) break;
        const session = this.store.get(this.activeSessionId);
        if (!session) break;
        await this.store.updateSessionSettings(this.activeSessionId, {
          ...session.settings,
          codebaseDirs: session.settings.codebaseDirs.filter((d) => d !== msg.dir),
        });
        this.pushState();
        break;
      }

      case 'openSettings':
        await vscode.commands.executeCommand('workbench.action.openSettings', 'poorMansClaude');
        break;
    }
  }

  private pushState(): void {
    if (!this.view) return;
    const sessions = this.store.list();
    if (this.activeSessionId && !sessions.find((s) => s.id === this.activeSessionId)) {
      this.activeSessionId = sessions[0]?.id ?? null;
    }
    const activeSession = sessions.find((s) => s.id === this.activeSessionId);
    const sessionSettingsErrors = activeSession
      ? resolveSessionSettings(activeSession.settings).errors
      : [];

    this.view.webview.postMessage({
      type: 'state',
      sessions,
      activeId: this.activeSessionId,
      sessionSettingsErrors,
    });
  }

  private postTurnUpdate(sessionId: string, turn: Turn): void {
    this.view?.webview.postMessage({ type: 'turnUpdate', sessionId, turn });
  }

  private postOutput(sessionId: string, turnId: string, text: string, stream: 'stdout' | 'stderr' | 'system'): void {
    this.view?.webview.postMessage({ type: 'appendOutput', sessionId, turnId, text, stream });
  }

  private async appendSystem(sessionId: string, turnId: string, text: string): Promise<void> {
    await this.store.appendOutput(sessionId, turnId, { text, stream: 'system' });
    this.postOutput(sessionId, turnId, text, 'system');
  }

  private async cancelActive(reason: string): Promise<void> {
    const job = this.activeJob;
    if (!job) return;
    job.cancelled = true;
    if (job.cancelPrompt) job.cancelPrompt();
    if (job.runner) job.runner.cancel();
    await this.store.setTurnStatus(job.sessionId, job.turnId, 'cancelled');
    await this.appendSystem(job.sessionId, job.turnId, `[cancelled] ${reason}`);
    const turn = this.store.get(job.sessionId)?.turns.find((t) => t.id === job.turnId);
    if (turn) {
      turn.status = 'cancelled';
      this.postTurnUpdate(job.sessionId, turn);
    }
    this.activeJob = null;
  }

  // Opens a VS Code QuickPick and returns the selected item's value,
  // or null if the user dismisses or the job is cancelled externally.
  private runQuickPick(items: PickItem[], title: string, job: ActiveJob): Promise<string | null> {
    return new Promise<string | null>((resolve) => {
      if (job.cancelled) { resolve(null); return; }

      const qp = vscode.window.createQuickPick<PickItem>();
      qp.title = title;
      qp.items = items;
      qp.ignoreFocusOut = true; // stays open when user switches to browser/Finder
      qp.placeholder = 'Select an option…';

      let settled = false;
      const settle = (val: string | null) => {
        if (settled) return;
        settled = true;
        job.cancelPrompt = undefined;
        qp.dispose();
        resolve(val);
      };

      // Allow external cancellation (Cancel button, notification Cancel)
      job.cancelPrompt = () => settle(null);

      qp.onDidAccept(() => settle(qp.selectedItems[0]?.value ?? null));
      qp.onDidHide(() => settle(null));
      qp.show();
    });
  }

  private async listShFiles(dir: string): Promise<Array<{ filePath: string; mtime: number }>> {
    try {
      const entries = await fs.promises.readdir(dir, { withFileTypes: true });
      const shFiles = entries.filter((e) => e.isFile() && e.name.endsWith('.sh'));
      const withStats = await Promise.all(
        shFiles.map(async (e) => {
          const fullPath = path.join(dir, e.name);
          const stat = await fs.promises.stat(fullPath);
          return { filePath: fullPath, mtime: stat.mtimeMs };
        }),
      );
      return withStats.sort((a, b) => b.mtime - a.mtime); // newest first
    } catch {
      return [];
    }
  }

  // Shows the two-option picker. Returns the resolved script path, or null if
  // the user dismisses or cancels. For the "choose from dir" option, includes a
  // Refresh entry so the user can rescan after downloading a file.
  private async promptForScript(
    downloadDir: string,
    expectedName: string,
    job: ActiveJob,
  ): Promise<string | null> {
    if (job.cancelled) return null;

    const method = await this.runQuickPick(
      [
        {
          label: '$(clippy) Paste from clipboard',
          description: 'Script content is already copied — the extension will write and run it',
          value: 'clipboard',
        },
        {
          label: '$(folder-opened) Choose from download directory',
          description: downloadDir,
          value: 'choose',
        },
      ],
      `Provide the generated script  ·  expected: ${expectedName}`,
      job,
    );

    if (!method || job.cancelled) return null;

    if (method === 'clipboard') {
      const content = await vscode.env.clipboard.readText();
      if (!content.trim()) {
        vscode.window.showErrorMessage(
          'Clipboard is empty — copy the script from Claude first, then try again.',
        );
        return null;
      }
      const dest = path.join(downloadDir, expectedName);
      try {
        await fs.promises.writeFile(dest, content, 'utf8');
      } catch (e: any) {
        vscode.window.showErrorMessage(`Failed to write script: ${e.message ?? e}`);
        return null;
      }
      return dest;
    }

    // "choose" — list files with a Refresh option at the top
    while (true) {
      if (job.cancelled) return null;

      const files = await this.listShFiles(downloadDir);
      const items: PickItem[] = [
        {
          label: '$(refresh) Refresh list',
          description: 'Rescan the directory after downloading the file',
          value: '__refresh__',
        },
      ];

      if (files.length === 0) {
        items.push({
          label: '$(warning) No .sh files found',
          description: downloadDir,
          value: '__empty__',
        });
      } else {
        for (const f of files) {
          const name = path.basename(f.filePath);
          items.push({
            label: name,
            description: new Date(f.mtime).toLocaleString(),
            detail: f.filePath,
            value: f.filePath,
          });
        }
      }

      const picked = await this.runQuickPick(
        items,
        `Select script to execute  ·  ${downloadDir}`,
        job,
      );

      if (!picked || picked === '__empty__' || job.cancelled) return null;
      if (picked === '__refresh__') continue; // rescan and redisplay
      return picked;
    }
  }

  private async handleSend(rawText: string): Promise<void> {
    const text = (rawText || '').trim();
    if (!text) return;
    if (!this.activeSessionId) {
      const globalSettings = readSettings();
      const s = await this.store.createSession({
        codebaseDirs: globalSettings.codebaseDirs,
        uploadDir: globalSettings.uploadDir,
        downloadDir: globalSettings.downloadDir,
      });
      this.activeSessionId = s.id;
    }
    const sessionId = this.activeSessionId!;

    const session = this.store.get(sessionId);
    if (!session) return;

    const settings = resolveSessionSettings(session.settings);
    if (settings.errors.length > 0) {
      vscode.window.showErrorMessage(`Session settings issue: ${settings.errors.join('; ')}`);
      return;
    }
    if (settings.codebaseDirs.length === 0) {
      vscode.window.showErrorMessage('No codebase directories configured for this session.');
      return;
    }

    const { baseName, fullName } = buildScriptName(text);
    const rollbackDir = path.join(os.homedir(), '.poor_mans_claude', baseName);

    const turn: Turn = {
      id: `t_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`,
      promptText: text,
      scriptName: fullName,
      output: [],
      status: 'pending',
      rollbackAvailable: false,
      createdAt: Date.now(),
    };
    await this.store.upsertTurn(sessionId, turn);
    this.postTurnUpdate(sessionId, turn);

    this.activeJob = { sessionId, turnId: turn.id, cancelled: false };
    const job = this.activeJob;

    await this.appendSystem(sessionId, turn.id, '[scanning codebase...]');
    const scan = await scanCodebases(settings.codebaseDirs, {
      maxFileBytes: settings.maxFileBytes,
      maxTotalBytes: settings.maxTotalBytes,
    });
    await this.appendSystem(
      sessionId,
      turn.id,
      `[scan complete] ${scan.files.length} file(s), ${scan.totalBytes} bytes, ${scan.skipped.length} skipped`,
    );
    if (scan.exceededTotalCap) {
      await this.appendSystem(
        sessionId,
        turn.id,
        `[warn] assembled prompt exceeds ${settings.maxTotalBytes} bytes — Claude may reject it`,
      );
    }

    const promptText = buildPrompt({
      userPrompt: text,
      scriptBaseName: baseName,
      rollbackDir,
      files: scan.files,
    });

    const estimatedTokens = Math.round(promptText.length / 3.5);
    if (estimatedTokens > 900_000) {
      await this.store.setTurnStatus(sessionId, turn.id, 'error');
      await this.appendSystem(
        sessionId,
        turn.id,
        `[error] estimated input tokens (~${estimatedTokens.toLocaleString()}) exceeds 900,000 — too close to the 1M token limit`,
      );
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'error'; this.postTurnUpdate(sessionId, fresh); }
      this.activeJob = null;
      vscode.window.showErrorMessage(
        `Estimated input tokens: ~${estimatedTokens.toLocaleString()}. This is too close to the 1 million token limit. ` +
        `Please reduce your codebase directories or exclude large files.`,
        { modal: true },
      );
      return;
    }

    const confirmed = await vscode.window.showInformationMessage(
      `Estimated input tokens: ~${estimatedTokens.toLocaleString()}. Continue?`,
      { modal: true },
      'Continue',
    );
    if (confirmed !== 'Continue') {
      await this.store.setTurnStatus(sessionId, turn.id, 'cancelled');
      await this.appendSystem(sessionId, turn.id, '[cancelled] user dismissed token confirmation');
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'cancelled'; this.postTurnUpdate(sessionId, fresh); }
      this.activeJob = null;
      return;
    }

    const promptFileName = `${baseName}.txt`;
    const promptFilePath = path.join(settings.uploadDir, promptFileName);
    try {
      await fs.promises.writeFile(promptFilePath, promptText, 'utf8');
    } catch (e: any) {
      await this.store.setTurnStatus(sessionId, turn.id, 'error');
      await this.appendSystem(sessionId, turn.id, `[error] writing prompt: ${e.message ?? e}`);
      this.activeJob = null;
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'error'; this.postTurnUpdate(sessionId, fresh); }
      return;
    }

    await this.store.setTurnStatus(sessionId, turn.id, 'awaiting-script', { promptFilePath });
    await this.appendSystem(sessionId, turn.id, `[prompt written] ${promptFilePath}`);
    await this.appendSystem(sessionId, turn.id, `[awaiting script] upload the prompt to Claude, then use the picker below`);
    {
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'awaiting-script'; this.postTurnUpdate(sessionId, fresh); }
    }

    // Non-blocking reminder notification — Cancel button also works from here
    void vscode.window.showInformationMessage(
      `Prompt saved to ${promptFilePath}. Upload it to Claude, then use the picker to provide the generated script.`,
      'Cancel',
    ).then((choice) => {
      if (choice === 'Cancel') void this.cancelActive('user cancelled from notification');
    });

    // Block until the user provides the script via the picker
    const found = await this.promptForScript(settings.downloadDir, fullName, job);

    if (job.cancelled) return; // cancelActive already handled everything
    if (!found) {
      // User dismissed the picker (Escape / Cancel) without cancelling explicitly
      await this.cancelActive('script picker dismissed');
      return;
    }

    await this.appendSystem(sessionId, turn.id, `[script provided] ${found}`);
    try {
      await makeExecutable(found);
    } catch (e: any) {
      await this.store.setTurnStatus(sessionId, turn.id, 'error');
      await this.appendSystem(sessionId, turn.id, `[error] chmod failed: ${e.message ?? e}`);
      this.activeJob = null;
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'error'; this.postTurnUpdate(sessionId, fresh); }
      return;
    }

    await this.store.setTurnStatus(sessionId, turn.id, 'running', { scriptPath: found });
    {
      const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turn.id);
      if (fresh) { fresh.status = 'running'; this.postTurnUpdate(sessionId, fresh); }
    }

    await this.runScriptForTurn(sessionId, turn.id, found, []);
  }

  private async runScriptForTurn(
    sessionId: string,
    turnId: string,
    scriptPath: string,
    args: string[],
  ): Promise<void> {
    return new Promise<void>((resolve) => {
      const job = this.activeJob;
      const handle = runScript(scriptPath, args, {
        onLine: (line, stream) => {
          void this.store.appendOutput(sessionId, turnId, { text: line, stream });
          this.postOutput(sessionId, turnId, line, stream);
        },
        onError: (err) => {
          void this.appendSystem(sessionId, turnId, `[error] ${err.message}`);
        },
        onExit: async (code) => {
          const isRollback = args.includes('--rollback');
          let finalStatus: Turn['status'];
          let finalRollbackAvailable: boolean;

          if (isRollback) {
            finalStatus = 'rolledback';
            finalRollbackAvailable = false;
          } else if (code === 0) {
            finalStatus = 'done';
            finalRollbackAvailable = true;
          } else {
            finalStatus = 'error';
            finalRollbackAvailable = false;
          }

          await this.store.setTurnStatus(sessionId, turnId, finalStatus, {
            rollbackAvailable: finalRollbackAvailable,
          });
          const exitMsg = isRollback
            ? `[rollback exited] code=${code}`
            : `[script exited] code=${code}`;
          await this.appendSystem(sessionId, turnId, exitMsg);

          const fresh = this.store.get(sessionId)?.turns.find((t) => t.id === turnId);
          if (fresh) {
            fresh.status = finalStatus;
            fresh.rollbackAvailable = finalRollbackAvailable;
            this.postTurnUpdate(sessionId, fresh);
          }
          if (this.activeJob && this.activeJob.turnId === turnId) this.activeJob = null;
          resolve();
        },
      });
      if (job) job.runner = handle;
    });
  }

  private async handleRollback(): Promise<void> {
    if (!this.activeSessionId) return;
    const session = this.store.get(this.activeSessionId);
    if (!session) return;
    const lastTurn = [...session.turns].reverse().find((t) => t.scriptPath && t.rollbackAvailable);
    if (!lastTurn || !lastTurn.scriptPath) {
      vscode.window.showWarningMessage('No script available to rollback.');
      return;
    }
    if (!fs.existsSync(lastTurn.scriptPath)) {
      vscode.window.showErrorMessage(`Script not found: ${lastTurn.scriptPath}`);
      return;
    }
    this.activeJob = { sessionId: session.id, turnId: lastTurn.id, cancelled: false };
    await this.store.setTurnStatus(session.id, lastTurn.id, 'running');
    await this.appendSystem(session.id, lastTurn.id, `[rollback] ${lastTurn.scriptPath} --rollback`);
    {
      const fresh = this.store.get(session.id)?.turns.find((t) => t.id === lastTurn.id);
      if (fresh) { fresh.status = 'running'; this.postTurnUpdate(session.id, fresh); }
    }
    await this.runScriptForTurn(session.id, lastTurn.id, lastTurn.scriptPath, ['--rollback']);
  }

  private renderHtml(webview: vscode.Webview): string {
    const csp = `default-src 'none'; img-src ${webview.cspSource} data:; style-src ${webview.cspSource} 'unsafe-inline'; script-src ${webview.cspSource};`;
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(this.ctx.extensionUri, 'media', 'style.css'));
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(this.ctx.extensionUri, 'media', 'webview.js'));
    return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta http-equiv="Content-Security-Policy" content="${csp}">
  <link rel="stylesheet" href="${styleUri}">
  <title>Poor Man's Claude</title>
</head>
<body>
  <div id="app">
    <aside id="sidebar">
      <header>
        <h2>Sessions</h2>
        <button id="newSessionBtn" title="New session">+ New</button>
      </header>
      <div id="sessionList"></div>
    </aside>
    <section id="main">
      <div id="statusBar"></div>
      <div id="settingsPanel"></div>
      <div id="turns"></div>
      <div id="composer">
        <textarea id="promptInput" placeholder="Describe the change… (Cmd/Ctrl+Enter to send)"></textarea>
        <div class="row">
          <button id="cancelBtn" class="secondary" disabled>Cancel</button>
          <button id="rollbackBtn" class="danger" disabled>Rollback</button>
          <button id="sendBtn" class="primary">Send</button>
        </div>
      </div>
    </section>
  </div>
  <script src="${scriptUri}"></script>
</body>
</html>`;
  }
}
