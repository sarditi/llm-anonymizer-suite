import * as vscode from 'vscode';

export interface OutputLine {
  text: string;
  stream: 'stdout' | 'stderr' | 'system';
}

export interface SessionSettings {
  codebaseDirs: string[];
  uploadDir: string;
  downloadDir: string;
}

export interface Turn {
  id: string;
  promptText: string;
  scriptName?: string;
  scriptPath?: string;
  promptFilePath?: string;
  output: OutputLine[];
  status: 'pending' | 'awaiting-script' | 'running' | 'done' | 'rolledback' | 'cancelled' | 'error';
  rollbackAvailable: boolean;
  createdAt: number;
}

export interface Session {
  id: string;
  title: string;
  createdAt: number;
  turns: Turn[];
  settings: SessionSettings;
}

const KEY = 'poorMansClaude.sessions';

export class SessionStore {
  constructor(private readonly ctx: vscode.ExtensionContext) {}

  list(): Session[] {
    const raw = this.ctx.globalState.get<any[]>(KEY, []);
    // Migrate pre-settings sessions
    return raw.map((s) => ({
      ...s,
      settings: s.settings ?? { codebaseDirs: [], uploadDir: '', downloadDir: '' },
    })) as Session[];
  }

  get(id: string): Session | undefined {
    return this.list().find((s) => s.id === id);
  }

  async createSession(initialSettings?: SessionSettings): Promise<Session> {
    const s: Session = {
      id: `s_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
      title: 'New session',
      createdAt: Date.now(),
      turns: [],
      settings: initialSettings ?? { codebaseDirs: [], uploadDir: '', downloadDir: '' },
    };
    const all = this.list();
    all.unshift(s);
    await this.ctx.globalState.update(KEY, all);
    return s;
  }

  async deleteSession(id: string): Promise<void> {
    const all = this.list().filter((s) => s.id !== id);
    await this.ctx.globalState.update(KEY, all);
  }

  async renameSession(id: string, title: string): Promise<void> {
    const all = this.list();
    const s = all.find((x) => x.id === id);
    if (!s) return;
    s.title = title;
    await this.ctx.globalState.update(KEY, all);
  }

  async updateSessionSettings(sessionId: string, settings: SessionSettings): Promise<void> {
    const all = this.list();
    const s = all.find((x) => x.id === sessionId);
    if (!s) return;
    s.settings = settings;
    await this.ctx.globalState.update(KEY, all);
  }

  async upsertTurn(sessionId: string, turn: Turn): Promise<void> {
    const all = this.list();
    const s = all.find((x) => x.id === sessionId);
    if (!s) return;
    const idx = s.turns.findIndex((t) => t.id === turn.id);
    if (idx === -1) s.turns.push(turn);
    else s.turns[idx] = turn;
    if ((s.title === 'New session' || !s.title) && turn.promptText) {
      s.title = turn.promptText.slice(0, 40).replace(/\s+/g, ' ').trim() || 'New session';
    }
    await this.ctx.globalState.update(KEY, all);
  }

  async appendOutput(sessionId: string, turnId: string, line: OutputLine): Promise<void> {
    const all = this.list();
    const s = all.find((x) => x.id === sessionId);
    if (!s) return;
    const t = s.turns.find((x) => x.id === turnId);
    if (!t) return;
    t.output.push(line);
    await this.ctx.globalState.update(KEY, all);
  }

  async setTurnStatus(
    sessionId: string,
    turnId: string,
    status: Turn['status'],
    extra: Partial<Turn> = {},
  ): Promise<void> {
    const all = this.list();
    const s = all.find((x) => x.id === sessionId);
    if (!s) return;
    const t = s.turns.find((x) => x.id === turnId);
    if (!t) return;
    t.status = status;
    Object.assign(t, extra);
    await this.ctx.globalState.update(KEY, all);
  }
}
