import * as fs from 'fs';
import * as path from 'path';

const DENY_DIRS = new Set<string>([
  'node_modules', '.git', '.svn', '.hg', '.idea', '.vscode',
  'dist', 'build', 'out', 'target', '.next', '.turbo', '.parcel-cache',
  '__pycache__', '.pytest_cache', '.mypy_cache', '.venv', 'venv', 'env',
  '.dart_tool', '.flutter-plugins',
  '.gradle', '.cache', 'coverage', '.nyc_output',
]);

const DENY_EXT = new Set<string>([
  '.exe','.dll','.so','.dylib','.a','.o','.class','.jar','.war','.pyc','.pyo','.pyd','.wasm',
  '.png','.jpg','.jpeg','.gif','.ico','.bmp','.tiff','.webp','.svg',
  '.pdf','.zip','.tar','.gz','.tgz','.7z','.rar','.bz2','.xz',
  '.mp3','.mp4','.mov','.avi','.mkv','.wav','.flac','.ogg',
  '.lock','.map',
  '.ttf','.otf','.woff','.woff2','.eot',
]);

const DENY_FILE_NAMES = new Set<string>([
  'package-lock.json','yarn.lock','pnpm-lock.yaml','poetry.lock','Pipfile.lock',
  'Cargo.lock','composer.lock','Gemfile.lock','pubspec.lock','go.sum',
  '.DS_Store','Thumbs.db',
]);

const DENY_NAME_PATTERNS: RegExp[] = [
  /\.g\.dart$/i,
  /\.freezed\.dart$/i,
  /\.mocks\.dart$/i,
  /\.flutter-plugins/i,
  /\.egg-info$/i,
];

export interface ScanOptions {
  maxFileBytes: number;
  maxTotalBytes: number;
}

export interface ScannedFile {
  fullPath: string;
  content: string;
  bytes: number;
}

export interface ScanResult {
  files: ScannedFile[];
  skipped: { path: string; reason: string }[];
  totalBytes: number;
  exceededTotalCap: boolean;
}

function isProbablyBinary(buf: Buffer): boolean {
  const len = Math.min(buf.length, 8192);
  if (len === 0) return false;
  let suspicious = 0;
  for (let i = 0; i < len; i++) {
    const b = buf[i];
    if (b === 0) return true;
    if (b < 9) suspicious++;
    else if (b > 13 && b < 32) suspicious++;
  }
  return suspicious / len > 0.3;
}

function shouldSkipDir(name: string): boolean {
  if (DENY_DIRS.has(name)) return true;
  if (name.endsWith('.egg-info')) return true;
  return false;
}

function shouldSkipFile(name: string): boolean {
  if (DENY_FILE_NAMES.has(name)) return true;
  const ext = path.extname(name).toLowerCase();
  if (DENY_EXT.has(ext)) return true;
  for (const re of DENY_NAME_PATTERNS) if (re.test(name)) return true;
  return false;
}

export async function scanCodebases(roots: string[], opts: ScanOptions): Promise<ScanResult> {
  const files: ScannedFile[] = [];
  const skipped: { path: string; reason: string }[] = [];
  let total = 0;
  let exceeded = false;

  const walk = async (dir: string) => {
    let entries: fs.Dirent[];
    try {
      entries = await fs.promises.readdir(dir, { withFileTypes: true });
    } catch (e: any) {
      skipped.push({ path: dir, reason: `readdir failed: ${e.message ?? e}` });
      return;
    }
    for (const ent of entries) {
      const full = path.join(dir, ent.name);
      if (ent.isSymbolicLink()) {
        skipped.push({ path: full, reason: 'symlink' });
        continue;
      }
      if (ent.isDirectory()) {
        if (shouldSkipDir(ent.name)) continue;
        await walk(full);
        continue;
      }
      if (!ent.isFile()) continue;
      if (shouldSkipFile(ent.name)) continue;

      let stat: fs.Stats;
      try {
        stat = await fs.promises.stat(full);
      } catch (e: any) {
        skipped.push({ path: full, reason: `stat failed: ${e.message ?? e}` });
        continue;
      }
      if (stat.size > opts.maxFileBytes) {
        skipped.push({ path: full, reason: `>${opts.maxFileBytes} bytes` });
        continue;
      }

      let buf: Buffer;
      try {
        buf = await fs.promises.readFile(full);
      } catch (e: any) {
        skipped.push({ path: full, reason: `read failed: ${e.message ?? e}` });
        continue;
      }
      if (isProbablyBinary(buf)) {
        skipped.push({ path: full, reason: 'binary' });
        continue;
      }
      const content = buf.toString('utf8');
      files.push({ fullPath: full, content, bytes: buf.length });
      total += buf.length;
      if (total > opts.maxTotalBytes) exceeded = true;
    }
  };

  for (const r of roots) {
    try {
      const st = await fs.promises.stat(r);
      if (!st.isDirectory()) {
        skipped.push({ path: r, reason: 'not a directory' });
        continue;
      }
    } catch (e: any) {
      skipped.push({ path: r, reason: `stat failed: ${e.message ?? e}` });
      continue;
    }
    await walk(r);
  }

  return { files, skipped, totalBytes: total, exceededTotalCap: exceeded };
}

export function formatCodebaseSection(files: ScannedFile[]): string {
  const parts: string[] = [];
  for (const f of files) {
    parts.push(`=== FILE_START: ${f.fullPath} ===`);
    parts.push(f.content.endsWith('\n') ? f.content.slice(0, -1) : f.content);
    parts.push(`=== FILE_END: ${f.fullPath} ===`);
    parts.push('');
  }
  return parts.join('\n');
}
