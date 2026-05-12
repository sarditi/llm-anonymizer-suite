import { spawn } from 'child_process';
import * as fs from 'fs';

export interface RunnerEvents {
  onLine: (line: string, stream: 'stdout' | 'stderr') => void;
  onExit: (code: number | null, signal: NodeJS.Signals | null) => void;
  onError: (err: Error) => void;
}

export interface RunHandle {
  cancel(): void;
}

export async function makeExecutable(scriptPath: string): Promise<void> {
  await fs.promises.chmod(scriptPath, 0o755);
}

export function runScript(scriptPath: string, args: string[], events: RunnerEvents): RunHandle {
  const child = spawn(scriptPath, args, {
    env: process.env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const wireStream = (stream: NodeJS.ReadableStream, label: 'stdout' | 'stderr') => {
    let buf = '';
    stream.setEncoding('utf8');
    stream.on('data', (chunk: string) => {
      buf += chunk;
      let idx: number;
      while ((idx = buf.indexOf('\n')) !== -1) {
        const line = buf.slice(0, idx).replace(/\r$/, '');
        buf = buf.slice(idx + 1);
        events.onLine(line, label);
      }
    });
    stream.on('end', () => {
      if (buf.length > 0) events.onLine(buf, label);
    });
  };

  if (child.stdout) wireStream(child.stdout, 'stdout');
  if (child.stderr) wireStream(child.stderr, 'stderr');

  child.on('error', (err) => events.onError(err));
  child.on('close', (code, signal) => events.onExit(code, signal));

  return {
    cancel() {
      try { child.kill('SIGTERM'); } catch { /* ignore */ }
    },
  };
}
