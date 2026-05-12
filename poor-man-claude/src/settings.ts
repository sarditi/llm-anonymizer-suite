import * as vscode from 'vscode';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';

export interface ResolvedSettings {
  codebaseDirs: string[];
  uploadDir: string;
  downloadDir: string;
  maxFileBytes: number;
  maxTotalBytes: number;
  errors: string[];
}

export function expandHome(p: string): string {
  if (!p) return p;
  if (p === '~') return os.homedir();
  if (p.startsWith('~/')) return path.join(os.homedir(), p.slice(2));
  return p;
}

export function toAbsolute(p: string): string {
  const expanded = expandHome(p);
  return path.isAbsolute(expanded) ? path.normalize(expanded) : path.resolve(expanded);
}

function checkDir(p: string, requireWrite: boolean): string | null {
  try {
    const st = fs.statSync(p);
    if (!st.isDirectory()) return `${p} is not a directory`;
    fs.accessSync(p, fs.constants.R_OK | (requireWrite ? fs.constants.W_OK : 0));
    return null;
  } catch (e: any) {
    return `${p} is not accessible: ${e.message ?? e}`;
  }
}

export function readSettings(): ResolvedSettings {
  const cfg = vscode.workspace.getConfiguration('poorMansClaude');
  const rawCodebase = cfg.get<string[]>('codebaseDirs') ?? [];
  const rawUpload = cfg.get<string>('uploadDir') ?? '';
  const rawDownload = cfg.get<string>('downloadDir') ?? '';
  const maxFileBytes = cfg.get<number>('maxFileBytes') ?? 262144;
  const maxTotalBytes = cfg.get<number>('maxTotalBytes') ?? 10485760;

  const errors: string[] = [];
  const codebaseDirs: string[] = [];

  for (const d of rawCodebase) {
    if (!d || !d.trim()) continue;
    const abs = toAbsolute(d.trim());
    const err = checkDir(abs, false);
    if (err) errors.push(`codebaseDirs: ${err}`);
    else codebaseDirs.push(abs);
  }

  let uploadDir = '';
  if (rawUpload && rawUpload.trim()) {
    uploadDir = toAbsolute(rawUpload.trim());
    const err = checkDir(uploadDir, true);
    if (err) errors.push(`uploadDir: ${err}`);
  } else {
    errors.push('uploadDir: not configured');
  }

  let downloadDir = '';
  if (rawDownload && rawDownload.trim()) {
    downloadDir = toAbsolute(rawDownload.trim());
    const err = checkDir(downloadDir, true);
    if (err) errors.push(`downloadDir: ${err}`);
  } else {
    errors.push('downloadDir: not configured');
  }

  return { codebaseDirs, uploadDir, downloadDir, maxFileBytes, maxTotalBytes, errors };
}

export function resolveSessionSettings(raw: {
  codebaseDirs: string[];
  uploadDir: string;
  downloadDir: string;
}): ResolvedSettings {
  const cfg = vscode.workspace.getConfiguration('poorMansClaude');
  const errors: string[] = [];
  const codebaseDirs: string[] = [];

  for (const d of raw.codebaseDirs) {
    if (!d || !d.trim()) continue;
    const abs = toAbsolute(d.trim());
    const err = checkDir(abs, false);
    if (err) errors.push(`codebaseDirs: ${err}`);
    else codebaseDirs.push(abs);
  }

  let uploadDir = '';
  if (raw.uploadDir && raw.uploadDir.trim()) {
    uploadDir = toAbsolute(raw.uploadDir.trim());
    const err = checkDir(uploadDir, true);
    if (err) errors.push(`uploadDir: ${err}`);
  } else {
    errors.push('uploadDir: not configured');
  }

  let downloadDir = '';
  if (raw.downloadDir && raw.downloadDir.trim()) {
    downloadDir = toAbsolute(raw.downloadDir.trim());
    const err = checkDir(downloadDir, true);
    if (err) errors.push(`downloadDir: ${err}`);
  } else {
    errors.push('downloadDir: not configured');
  }

  return {
    codebaseDirs,
    uploadDir,
    downloadDir,
    maxFileBytes: cfg.get<number>('maxFileBytes') ?? 262144,
    maxTotalBytes: cfg.get<number>('maxTotalBytes') ?? 10485760,
    errors,
  };
}

export async function updateConfig(key: string, value: unknown): Promise<void> {
  const cfg = vscode.workspace.getConfiguration('poorMansClaude');
  await cfg.update(key, value, vscode.ConfigurationTarget.Global);
}

export async function pickDirectoryAndStore(
  key: 'uploadDir' | 'downloadDir',
  label: string,
  requireWrite: boolean,
): Promise<string | null> {
  const picked = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    openLabel: `Select ${label}`,
  });
  if (!picked || picked.length === 0) return null;
  const abs = toAbsolute(picked[0].fsPath);
  const err = checkDir(abs, requireWrite);
  if (err) {
    vscode.window.showErrorMessage(`Cannot use ${label}: ${err}`);
    return null;
  }
  await updateConfig(key, abs);
  return abs;
}

export async function pickDirectory(label: string, requireWrite: boolean): Promise<string | null> {
  const picked = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    openLabel: label,
  });
  if (!picked || picked.length === 0) return null;
  const abs = toAbsolute(picked[0].fsPath);
  const err = checkDir(abs, requireWrite);
  if (err) {
    vscode.window.showErrorMessage(`Cannot use directory: ${err}`);
    return null;
  }
  return abs;
}

export async function addCodebaseDir(): Promise<string | null> {
  const picked = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    openLabel: 'Add codebase directory',
  });
  if (!picked || picked.length === 0) return null;
  const abs = toAbsolute(picked[0].fsPath);
  const err = checkDir(abs, false);
  if (err) {
    vscode.window.showErrorMessage(`Cannot add codebase dir: ${err}`);
    return null;
  }
  const cfg = vscode.workspace.getConfiguration('poorMansClaude');
  const current = (cfg.get<string[]>('codebaseDirs') ?? []).map(toAbsolute);
  if (current.includes(abs)) {
    vscode.window.showInformationMessage(`${abs} is already a codebase dir`);
    return abs;
  }
  current.push(abs);
  await cfg.update('codebaseDirs', current, vscode.ConfigurationTarget.Global);
  return abs;
}

export async function removeCodebaseDir(p: string): Promise<void> {
  const cfg = vscode.workspace.getConfiguration('poorMansClaude');
  const current = (cfg.get<string[]>('codebaseDirs') ?? []).map(toAbsolute);
  const next = current.filter((x) => x !== toAbsolute(p));
  await cfg.update('codebaseDirs', next, vscode.ConfigurationTarget.Global);
}
