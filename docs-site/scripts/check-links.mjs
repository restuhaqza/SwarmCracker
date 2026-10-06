// Validate internal links in the Starlight content tree before/after build.
// Maps every content file to its route, then checks each internal markdown link
// target resolves to a known route or a real file under public/.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import path from 'node:path';

const DOCS = 'src/content/docs';
const PUBLIC = 'public';

/** @param {string} dir @param {string[]} out */
function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const p = path.join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const files = walk(DOCS).filter((f) => /\.(md|mdx)$/.test(f));

// content file -> route
const routes = new Set(['/']);
const routeOf = (f) => {
  let rel = path.relative(DOCS, f).replace(/\.(md|mdx)$/, '');
  if (rel === 'index') return '/';
  if (rel.endsWith('/index')) rel = rel.slice(0, -'/index'.length);
  return '/' + rel + '/';
};
for (const f of files) routes.add(routeOf(f));

const publicFiles = new Set();
if (existsSync(PUBLIC)) for (const f of walk(PUBLIC)) publicFiles.add('/' + path.relative(PUBLIC, f));

const problems = [];
const linkRe = /\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g;

for (const f of files) {
  const text = readFileSync(f, 'utf8');
  // strip fenced code blocks
  const body = text.replace(/```[\s\S]*?```/g, '');
  let m;
  while ((m = linkRe.exec(body))) {
    const href = m[1];
    if (/^(https?:|mailto:|tel:|#)/.test(href)) continue;
    const target = href.split('#')[0];
    if (!target) continue;
    if (target.startsWith('/')) {
      const norm = target.endsWith('/') ? target : target + '/';
      if (!routes.has(norm) && !publicFiles.has(target) && !routes.has(target))
        problems.push(`${f}  ->  ${href}`);
    } else {
      // relative link: resolve against the file's directory
      const resolved = path.normalize(path.join(path.dirname(f), target));
      if (!existsSync(resolved) && !existsSync(resolved.replace(/\.(md|mdx)$/, '.md')))
        problems.push(`${f}  ->  ${href}  (relative, not found)`);
    }
  }
}

if (problems.length) {
  console.log(`✗ ${problems.length} broken internal link(s):`);
  for (const p of problems) console.log('  ' + p);
  process.exit(1);
} else {
  console.log(`✓ all internal links resolve (${files.length} files, ${routes.size} routes)`);
}
