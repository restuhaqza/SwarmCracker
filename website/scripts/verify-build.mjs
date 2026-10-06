import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';

const DIST = new URL('../dist/', import.meta.url).pathname;

// Later tasks append to these. Each entry must appear verbatim.
export const REQUIRED_TEXT = [
  'SwarmCracker',
  'Firecracker microVMs with SwarmKit orchestration',
  'rel="canonical" href="https://swarmcracker.com/"',
  'Read the docs',
  '$ swarmcracker service create --name web --image nginx:alpine --replicas 3',
  'service &quot;web&quot; converged (3/3 replicas)',
  '87%',
  '70/70',
  'Per-VM kernel',
  'Install SwarmCracker',
  'SwarmCracker vs. Docker vs. Kubernetes',
  'curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash',
  'swarmcracker service create --name web --image nginx:alpine --replicas 3',
  'Hardware-enforced boundaries',
  'Getting Started',
];
export const REQUIRED_LINKS = [
  'https://github.com/restuhaqza/swarmcracker',
  'https://github.com/restuhaqza/swarmcracker/releases',
  'https://github.com/restuhaqza/swarmcracker/blob/main/LICENSE',
  'https://docs.swarmcracker.com',
  '#quickstart',
  '/swarmcracker-architecture.svg',
  'https://docs.swarmcracker.com/user/getting-started/',
  'https://docs.swarmcracker.com/user/reference/cli/',
  'https://docs.swarmcracker.com/user/guides/networking/',
  'https://docs.swarmcracker.com/user/guides/security/',
];

export function runChecks(html, css) {
  const failures = [];
  for (const t of REQUIRED_TEXT) if (!html.includes(t)) failures.push(`missing text: ${t}`);
  for (const l of REQUIRED_LINKS) if (!html.includes(l)) failures.push(`missing link: ${l}`);

  const h1s = [...html.matchAll(/<h1\b/gi)].length;
  if (h1s !== 1) failures.push(`expected exactly one <h1>, found ${h1s}`);

  const insecure = [...html.matchAll(/href="http:\/\/[^"]+"/gi)].map((m) => m[0]);
  if (insecure.length) failures.push(`insecure links: ${insecure.join(', ')}`);

  for (const id of [...html.matchAll(/data-copy-target="#([^"]+)"/gi)].map((m) => m[1])) {
    if (!html.includes(`id="${id}"`)) failures.push(`copy target not found: #${id}`);
  }
  if (html.includes('data-copy-target') && !/prefers-reduced-motion/.test(css)) {
    failures.push('reduced-motion guard missing from built CSS');
  }
  return failures;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const indexPath = join(DIST, 'index.html');
  if (!existsSync(indexPath)) { console.error('dist/index.html not found — run npm run build'); process.exit(1); }
  const html = readFileSync(indexPath, 'utf8');
  const cssDir = join(DIST, '_astro');
  const fileCss = existsSync(cssDir)
    ? readdirSync(cssDir).filter((f) => f.endsWith('.css')).map((f) => readFileSync(join(cssDir, f), 'utf8')).join('\n')
    : '';
  // Also collect inline <style> blocks from index.html (Astro may inline small stylesheets).
  const inlineCss = [...html.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/gi)].map((m) => m[1]).join('\n');
  const css = fileCss + '\n' + inlineCss;
  const failures = runChecks(html, css);
  if (failures.length) { console.error('verify-build FAILED:\n- ' + failures.join('\n- ')); process.exit(1); }
  console.log('verify-build OK');
}
