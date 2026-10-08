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
  'Per-VM kernel',
  'Install SwarmCracker',
  'SwarmCracker vs. Docker vs. Kubernetes',
  'curl -fsSL https://swarmcracker.com/install.sh | sudo bash',
  'swarmcracker service create --name web --image nginx:alpine --replicas 3',
  'Hardware-enforced boundaries',
  'Getting Started',
  'rel="icon" href="/favicon.svg"',
  'property="og:image" content="https://swarmcracker.com/og-image.png"',
  'property="og:image:width" content="1200"',
  'property="og:image:height" content="630"',
  'property="og:image:alt"',
  'property="og:site_name" content="SwarmCracker"',
  'name="theme-color" content="#E8E6E0"',
  'class="skip-link"',
  'id="main"',
];
export const REQUIRED_LINKS = [
  'https://github.com/restuhaqza/swarmcracker',
  'https://github.com/restuhaqza/swarmcracker/releases',
  'https://github.com/restuhaqza/swarmcracker/blob/main/LICENSE',
  'https://docs.swarmcracker.com',
  '#quickstart',
  '/swarmcracker-architecture.svg',
  'https://docs.swarmcracker.com/getting-started/',
  'https://docs.swarmcracker.com/reference/cli/',
  'https://docs.swarmcracker.com/guides/networking/',
  'https://docs.swarmcracker.com/guides/security/',
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

  // Self-hosted fonts: preloads in the HTML, @font-face in the CSS.
  const fontPreloadHrefs = [...html.matchAll(/<link\b[^>]*rel="preload"[^>]*as="font"[^>]*>/gi)]
    .map((m) => /href="([^"]+)"/.exec(m[0])?.[1] ?? '');
  for (const font of ['bigshoulders-800', 'archivo-400', 'archivo-600', 'b612-mono-400']) {
    if (!fontPreloadHrefs.includes(`/fonts/${font}.woff2`)) {
      failures.push(`missing font preload link: /fonts/${font}.woff2`);
    }
    if (!css.includes(`/fonts/${font}.woff2`)) {
      failures.push(`@font-face src missing from built CSS: /fonts/${font}.woff2`);
    }
  }
  if (!/@font-face/.test(css)) failures.push('@font-face rules missing from built CSS');
  if (!/font-display:\s*swap/.test(css)) failures.push('font-display: swap missing from built CSS');
  if (!css.includes('scroll-margin-top')) {
    failures.push('scroll-margin-top (sticky-header anchor offset) missing from built CSS');
  }

  // Nav must ship a single GitHub entry — no dead star-count UI.
  if (html.includes('star-count')) failures.push('dead star-count markup must not ship');
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
