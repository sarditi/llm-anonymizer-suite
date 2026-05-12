import * as fs from 'fs';
import * as path from 'path';

export interface PollerHandle {
  cancel(): void;
  promise: Promise<string | null>;
}

export interface PollerOptions {
  dir: string;
  fileName: string;
  pollMs?: number;
  stableMs?: number;
}

export function startPoller(opts: PollerOptions): PollerHandle {
  const pollMs = opts.pollMs ?? 1500;
  const stableMs = opts.stableMs ?? 600;
  const fullPath = path.join(opts.dir, opts.fileName);

  let cancelled = false;
  let watcher: fs.FSWatcher | null = null;
  let interval: NodeJS.Timeout | null = null;
  let resolve: (v: string | null) => void = () => {};
  let resolved = false;

  const promise = new Promise<string | null>((res) => {
    resolve = res;
  });

  const finish = (val: string | null) => {
    if (resolved) return;
    resolved = true;
    if (watcher) { try { watcher.close(); } catch {} watcher = null; }
    if (interval) { clearInterval(interval); interval = null; }
    resolve(val);
  };

  const checkStableThenResolve = async () => {
    try {
      const s1 = await fs.promises.stat(fullPath);
      if (!s1.isFile()) return;
      await new Promise((r) => setTimeout(r, stableMs));
      if (cancelled) return;
      const s2 = await fs.promises.stat(fullPath);
      if (s2.size === s1.size && s2.mtimeMs === s1.mtimeMs) {
        finish(fullPath);
      }
    } catch {
      // not present yet
    }
  };

  // fs.watch
  try {
    watcher = fs.watch(opts.dir, (_event, filename) => {
      if (cancelled) return;
      if (!filename) {
        void checkStableThenResolve();
        return;
      }
      if (path.basename(filename.toString()) === opts.fileName) {
        void checkStableThenResolve();
      }
    });
    watcher.on('error', () => { /* ignore; interval will cover it */ });
  } catch {
    // ignore; interval covers it
  }

  // interval fallback
  interval = setInterval(() => {
    if (cancelled) return;
    void checkStableThenResolve();
  }, pollMs);

  // also check immediately
  void checkStableThenResolve();

  return {
    cancel() {
      cancelled = true;
      finish(null);
    },
    promise,
  };
}
