const STOPWORDS = new Set<string>([
  'the','a','an','and','or','but','if','then','else','when','at','from','by','on','off',
  'for','in','out','to','of','as','is','it','its','this','that','these','those','was',
  'were','be','been','being','have','has','had','do','does','did','will','would','should',
  'can','could','may','might','must','shall','i','you','he','she','they','we','them','us',
  'my','your','his','her','their','our','me','him','also','not','no','yes','so','too',
  'very','just','only','than','because','while','about','into','over','under','again',
  'with','without','within','any','some','all','each','every','more','most','less','few',
  'such','same','other','others','here','there','where','what','which','who','whom','how',
  'why','please','make','use','using','used','need','needs','want','wants','create','add',
  'adding','remove','removing','update','updating','change','changes','changing','modify',
  'modifying','make','makes','set','sets','setting','also','etc','via','per','file','files',
  'code','codebase','script','scripts','prompt','user','test','tests','run','running',
  'between','below','above','before','after','once','must','should','let','make','make',
]);

export interface SummaryResult {
  words: string[];
  slug: string;
}

export function summarizeFourWords(text: string): SummaryResult {
  const cleaned = (text || '').toLowerCase().replace(/[^a-z0-9_\s-]+/g, ' ');
  const tokens = cleaned.split(/\s+/).filter(Boolean);
  const counts = new Map<string, { freq: number; firstIdx: number }>();
  tokens.forEach((tok, idx) => {
    if (tok.length < 3 || tok.length > 30) return;
    if (STOPWORDS.has(tok)) return;
    if (/^\d+$/.test(tok)) return;
    const existing = counts.get(tok);
    if (existing) existing.freq += 1;
    else counts.set(tok, { freq: 1, firstIdx: idx });
  });

  const ranked = Array.from(counts.entries())
    .sort((a, b) => {
      if (b[1].freq !== a[1].freq) return b[1].freq - a[1].freq;
      return a[1].firstIdx - b[1].firstIdx;
    })
    .map(([w]) => w);

  while (ranked.length < 4) ranked.push('task');
  const top = ranked.slice(0, 4);
  const slug = top.map((w) => w.replace(/[^a-z0-9]/g, '')).filter(Boolean).join('_') || 'task_task_task_task';
  return { words: top, slug };
}

export function timestamp(d: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `_${pad(d.getHours())}_${pad(d.getMinutes())}_${pad(d.getSeconds())}`
  );
}

export function buildScriptName(prompt: string, when: Date = new Date()): {
  baseName: string;
  fullName: string;
  ts: string;
} {
  const { slug } = summarizeFourWords(prompt);
  const ts = timestamp(when);
  const baseName = `${slug}_${ts}`;
  return { baseName, fullName: `${baseName}.sh`, ts };
}
