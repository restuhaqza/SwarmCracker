# SwarmCracker Landing Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a dark-first, accessible, static landing page for SwarmCracker at `swarmcracker.com` that drives installs and routes readers to the docs.

**Architecture:** A single Astro 5 static page built from small, section-scoped components. All copy, links, and stats live in one `src/data/site.ts` module. Styling is scoped CSS over shared brand tokens. Deployment is Cloudflare Pages from the in-repo `website/` directory; the existing MkDocs site moves to `docs.swarmcracker.com`.

**Tech Stack:** Astro 5, TypeScript, plain scoped CSS (+ design tokens), minimal vanilla JS, Node `verify-build.mjs` content checks, Cloudflare Pages.

**Spec:** `docs/superpowers/specs/2026-10-06-swarmcracker-landing-page-design.md`

## Global Constraints

- Astro 5, `output: 'static'`; site URL `https://swarmcracker.com`.
- No framework islands. Vanilla JS allowed only for the copy-to-clipboard buttons.
- No CSS framework (no Tailwind). Scoped component CSS + shared tokens only.
- Dark theme only in this release; no light mode.
- Brand tokens: primary `#FF6B35`, light `#FF8E53`, dark `#E55A2B`; fonts Inter (text) and JetBrains Mono (code).
- Every documentation link points to `https://docs.swarmcracker.com`.
- Site source lives in `website/` in this repo. Cloudflare Pages root `website`, build `npm run build`, output `dist`.
- Content is single-sourced in `website/src/data/site.ts`; components import it, they do not hardcode copy.
- Semantic landmarks, exactly one `<h1>`, logical heading order, keyboard reachable, visible focus, WCAG AA contrast.
- Animation only under `prefers-reduced-motion: no-preference`.
- Core content and links must render with JavaScript disabled.
- Lighthouse desktop and mobile ≥ 95 in all four categories.

## Review Focus

The spec implies these inputs but no single task's tests fully cover them; each is pinned to a test in the task that owns the code.

1. **Narrow viewports (≤ 360 px):** a long `curl … | sudo bash` command must scroll inside its code block, never force page-wide horizontal scrolling.
2. **`prefers-reduced-motion: reduce`:** the hero terminal must show its final lines instantly, with no animation and no hidden content.
3. **JavaScript disabled:** navigation, anchor links, and every quickstart command must stay visible and usable; only copy buttons may disappear.
4. **~768 px widths:** code blocks and the terminal must not overflow, and each copy button must still map to its own command.
5. **Dark-theme contrast:** accent-orange text, muted body text, and focus rings must meet WCAG AA against the dark background.

---

### Task 1: Scaffold the Astro site and verification harness

**Files:**
- Create: `website/package.json`
- Create: `website/astro.config.mjs`
- Create: `website/tsconfig.json`
- Create: `website/.gitignore`
- Create: `website/src/styles/tokens.css`
- Create: `website/src/layouts/BaseLayout.astro`
- Create: `website/src/pages/index.astro`
- Create: `website/public/robots.txt`
- Create: `website/scripts/verify-build.mjs`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - npm scripts `dev`, `build`, `preview`, `check`, `verify` (verify = `check` + `build` + `node scripts/verify-build.mjs`).
  - `BaseLayout.astro` with props `{ title: string; description: string; canonical: string }` and a default `<slot />`.
  - `verify-build.mjs` exporting `const REQUIRED_TEXT: string[]`, `const REQUIRED_LINKS: string[]`, `function runChecks(html: string, css: string): string[]` (returns failure messages), and a CLI entry that prints failures and `process.exit(1)` when non-empty. Later tasks append to the two arrays.

- [ ] **Step 1: Write the harness with the placeholder expectations**

Create `website/scripts/verify-build.mjs`:

```js
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';

const DIST = new URL('../dist/', import.meta.url).pathname;

// Later tasks append to these. Each entry must appear verbatim.
export const REQUIRED_TEXT = ['SwarmCracker'];
export const REQUIRED_LINKS = [];

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
  const css = existsSync(cssDir)
    ? readdirSync(cssDir).filter((f) => f.endsWith('.css')).map((f) => readFileSync(join(cssDir, f), 'utf8')).join('\n')
    : '';
  const failures = runChecks(html, css);
  if (failures.length) { console.error('verify-build FAILED:\n- ' + failures.join('\n- ')); process.exit(1); }
  console.log('verify-build OK');
}
```

- [ ] **Step 2: Run the harness to confirm it fails**

Run: `cd website && node scripts/verify-build.mjs`
Expected: FAIL with `dist/index.html not found`.

- [ ] **Step 3: Create the project configuration**

`website/package.json`:

```json
{
  "name": "swarmcracker-website",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "astro dev",
    "build": "astro build",
    "preview": "astro preview",
    "check": "astro check",
    "verify": "astro check && astro build && node scripts/verify-build.mjs"
  },
  "dependencies": { "astro": "^5.0.0" },
  "devDependencies": { "@astrojs/check": "^0.9.0", "typescript": "^5.6.0" }
}
```

`website/astro.config.mjs`:

```js
import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://swarmcracker.com',
  output: 'static',
  build: { format: 'directory' },
});
```

`website/tsconfig.json`:

```json
{ "extends": "astro/tsconfigs/strict" }
```

`website/.gitignore`:

```
node_modules/
dist/
.astro/
```

Append to the repository root `.gitignore`:

```
# Website
website/node_modules/
website/dist/
website/.astro/
```

- [ ] **Step 4: Create tokens, layout, page, and robots**

`website/src/styles/tokens.css` — define the brand tokens (reused from `docs/stylesheets/extra.css`) and the dark palette: `--sc-orange: #FF6B35`, `--sc-orange-light: #FF8E53`, `--sc-orange-dark: #E55A2B`, `--sc-bg: #0B0B0F`, `--sc-surface: #14141B`, `--sc-border: #23232E`, `--sc-text: #F2F2F5`, `--sc-text-muted: #A0A0B0`, `--sc-font-text: 'Inter', system-ui, sans-serif`, `--sc-font-code: 'JetBrains Mono', ui-monospace, monospace`. Include a global `@media (prefers-reduced-motion: reduce)` block that disables animation/transition durations.

`website/src/layouts/BaseLayout.astro` — import `../styles/tokens.css`; accept the three props; render `<!doctype html>`, `<html lang="en">`, a `<head>` with charset, viewport, `<title>{title}</title>`, meta description, `<link rel="canonical" href={canonical}>`, and a `<main><slot /></main>`. Include the JS flag script `<script is:inline>document.documentElement.classList.add('js')</script>` in the head.

`website/src/pages/index.astro` — wrap a minimal placeholder in `BaseLayout` with the site title/description/canonical and an `<h1>SwarmCracker</h1>`.

`website/public/robots.txt`:

```
User-agent: *
Allow: /
Sitemap: https://swarmcracker.com/sitemap.xml
```

- [ ] **Step 5: Install and run checks**

Run: `cd website && npm install`
Then: `npm run check`
Expected: no errors.
Then: `npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 6: Commit**

```bash
git add website .gitignore
git commit -m "feat(website): scaffold Astro landing site with build verification"
```

---

### Task 2: Content data module and document head

**Files:**
- Create: `website/src/data/site.ts`
- Modify: `website/src/layouts/BaseLayout.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `BaseLayout` props from Task 1.
- Produces: `website/src/data/site.ts` exporting:
  - `type IconName = 'kernel' | 'swarm' | 'kvm' | 'boot' | 'network' | 'update'`
  - `interface NavItem { label: string; href: string }`
  - `interface Stat { value: string; label: string }`
  - `interface Feature { icon: IconName; title: string; body: string }`
  - `interface Step { title: string; body: string }`
  - `interface QuickstartCmd { label: string; code: string }`
  - `interface ComparisonRow { aspect: string; swarmcracker: string; docker: string; kubernetes: string }`
  - `interface SecurityPoint { title: string; body: string }`
  - `interface DocsCard { title: string; body: string; href: string }`
  - `const links: { github; releases; docs; license }` (docs = `https://docs.swarmcracker.com`)
  - `const site: { name; title; description; canonical; version }`
  - `const nav: NavItem[]`, `const stats: Stat[]`, `const features: Feature[]`, `const steps: Step[]`, `const quickstart: QuickstartCmd[]`, `const comparison: ComparisonRow[]`, `const security: SecurityPoint[]`, `const docsCards: DocsCard[]`, `const terminalLines: string[]`.

- [ ] **Step 1: Add head assertions to the harness and confirm failure**

Append to `REQUIRED_TEXT` in `website/scripts/verify-build.mjs`: `'Firecracker microVMs with SwarmKit orchestration'` and `'rel="canonical" href="https://swarmcracker.com/"'`.

Run: `cd website && npm run verify`
Expected: FAIL listing the missing description and canonical link.

Note: the `https://docs.swarmcracker.com` link assertion is added in Task 3, which is where the link is first rendered.

- [ ] **Step 2: Write `site.ts` with the real values**

Populate the arrays with values fixed by the spec and `README.md`:
- `stats`: `87%` / test coverage, `70/70` / E2E tests passing, `47` / security & review items addressed, `~100 ms` / microVM boot, `Apache 2.0` / license.
- `features`: per-VM kernel, SwarmKit compatible, KVM hardware isolation, ~100 ms boot, VXLAN cross-node networking, rolling updates (one `IconName` each).
- `quickstart`: the exact commands from the README — `curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash`; `sudo swarmcracker setup check`; `sudo swarmcracker setup install --download-kernel --download-rootfs`; `sudo swarmcracker setup network`; `sudo swarmcracker setup config --non-interactive`; `sudo swarmcracker cluster init --advertise-addr 192.168.1.10:4242`; `swarmcracker service create --name web --image nginx:alpine --replicas 3`.
- `docsCards`: Getting Started → `https://docs.swarmcracker.com/user/getting-started/`, CLI Reference → `.../user/reference/cli/`, Networking → `.../user/guides/networking/`, Security → `.../user/guides/security/`.
- `site.description`: `Firecracker microVMs with SwarmKit orchestration — Docker Swarm UX with hardware-isolated VMs.`
- `site.canonical`: `https://swarmcracker.com/`.
- `terminalLines`: the sequence showing a service being deployed and VMs starting.

- [ ] **Step 3: Use the data in `BaseLayout` and `index`**

Import `site` into `BaseLayout.astro`; keep the props but have `index.astro` pass `site.title`, `site.description`, `site.canonical`. Add Open Graph tags (`og:title`, `og:description`, `og:type=website`, `og:url`) and `twitter:card=summary_large_image` using the same values.

- [ ] **Step 4: Verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 5: Commit**

```bash
git add website
git commit -m "feat(website): add single-source content module and document head"
```

---

### Task 3: Navigation and footer shell

**Files:**
- Create: `website/src/components/Nav.astro`
- Create: `website/src/components/Footer.astro`
- Modify: `website/src/layouts/BaseLayout.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `nav`, `links`, `site` from `site.ts`.
- Produces: `Nav.astro` and `Footer.astro`, both propless (they import `site.ts`). `Nav` renders a sticky `<header>` containing a `<nav>` with the section anchor links, an external GitHub link, and a "Get Started" link to `#quickstart`. Mobile navigation uses a `<details>`/`<summary>` disclosure so it works without JavaScript. `Footer` renders license, GitHub, releases, and docs links.

- [ ] **Step 1: Extend the harness and confirm failure**

Append the nav/footer link hrefs to `REQUIRED_LINKS`: `'https://github.com/restuhaqza/SwarmCracker'`, `'https://github.com/restuhaqza/SwarmCracker/releases'`, `'https://github.com/restuhaqza/SwarmCracker/blob/main/LICENSE'`, `'https://docs.swarmcracker.com'`, and `'#quickstart'`.

Run: `cd website && npm run verify`
Expected: FAIL listing the missing links.

- [ ] **Step 2: Implement `Nav.astro` and `Footer.astro`**

`Nav` renders the Docs link (`links.docs`), the GitHub link, and the "Get Started" link. Add the skip link `<a href="#main" class="skip-link">Skip to content</a>` as the first focusable element in `Nav`, and change `BaseLayout`'s `<main>` to `<main id="main">`. Omit `aria-current` (a static page has no active hash link). The GitHub link reserves fixed width for an optional star count.

- [ ] **Step 3: Wire into `index.astro`**

Render `<Nav />` above the `<main>` content and `<Footer />` after it, inside `BaseLayout`.

- [ ] **Step 4: Verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 5: Commit**

```bash
git add website
git commit -m "feat(website): add site navigation and footer"
```

---

### Task 4: Hero and terminal

**Files:**
- Create: `website/src/components/Hero.astro`
- Create: `website/src/components/Terminal.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `site`, `links`, `quickstart`, `terminalLines`.
- Produces: `Hero.astro` (propless) rendering the single `<h1>`, subhead, an **Install** CTA linking to `#quickstart`, and a **Read the docs** CTA linking to `links.docs`; it embeds `<Terminal />`. `Terminal.astro` renders every line of `terminalLines` as static text in the HTML, revealed with per-line CSS animation delays. The placeholder `<h1>` from Task 1 is removed from `index.astro`.

- [ ] **Step 1: Add hero + reduced-motion assertions and confirm failure**

Append to `REQUIRED_TEXT`: `'Read the docs'` and the first and last entries of `terminalLines`. Append `'href="https://docs.swarmcracker.com'` to `REQUIRED_LINKS`.

Run: `cd website && npm run verify`
Expected: FAIL listing the missing hero text.

- [ ] **Step 2: Implement `Terminal.astro` (reduced-motion first)**

Render the lines inside `<pre class="terminal"><code>`. Animate with `--i` custom-property delays and `animation: reveal ...`; add `@media (prefers-reduced-motion: reduce) { .terminal__line { opacity: 1; animation: none } }` so lines are always fully visible. Give `.terminal` `overflow-x: auto` and `max-width: 100%` so long commands scroll inside the block at ≤ 360 px (Review Focus 1). Because `verify-build.mjs` already checks for `data-copy-target` + `prefers-reduced-motion`, also add the reduced-motion rule now even though copy buttons arrive in Task 7.

Verify Review Focus 2 manually: `npm run dev`, toggle OS "reduce motion", reload, and confirm the terminal appears fully rendered with no animation.

- [ ] **Step 3: Implement `Hero.astro`**

Use `<h1>` for the headline (exactly one on the page), an `<h2 class="visually-hidden">` is not needed; subhead is a `<p>`. The two CTAs are `<a>` elements styled as buttons. Add `text-wrap: balance` for the headline.

- [ ] **Step 4: Update `index.astro` and verify**

Remove the placeholder `<h1>`; render `<Hero />`.

Run: `cd website && npm run verify`
Expected: `verify-build OK` (one `<h1>` on the page).

- [ ] **Step 5: Commit**

```bash
git add website
git commit -m "feat(website): add hero section and animated terminal with reduced-motion support"
```

---

### Task 5: Stat strip and feature grid

**Files:**
- Create: `website/src/components/StatStrip.astro`
- Create: `website/src/components/FeatureGrid.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `stats`, `features`, `IconName`.
- Produces: `StatStrip.astro` (propless) rendering each stat as `<div><span class="stat__value">…</span><span class="stat__label">…</span></div>` in a `<section aria-labelledby="stats-heading">` with an `<h2 id="stats-heading">`. `FeatureGrid.astro` (propless) maps each feature to a card; it holds a local `const icons: Record<IconName, string>` of inline SVG markup and renders the matching icon with `aria-hidden="true"`.

- [ ] **Step 1: Add assertions and confirm failure**

Append to `REQUIRED_TEXT`: `'87%'`, `'70/70'`, and the first feature title from `features`.

Run: `cd website && npm run verify`
Expected: FAIL.

- [ ] **Step 2: Implement `StatStrip.astro` and `FeatureGrid.astro`**

Each is a `<section>` with an `<h2>`; cards use a CSS grid with `grid-template-columns: repeat(auto-fit, minmax(…, 1fr))` so they collapse to one column on mobile. Feature body text uses `--sc-text-muted`, which must pass AA (Review Focus 5) — the final contrast check lives in Task 9.

- [ ] **Step 3: Update `index.astro` and verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 4: Commit**

```bash
git add website
git commit -m "feat(website): add stat strip and feature grid"
```

---

### Task 6: How it works and comparison

**Files:**
- Create: `website/src/components/HowItWorks.astro`
- Create: `website/src/components/Comparison.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`
- Copy: `docs/architecture/swarmcracker-architecture.svg` → `website/public/swarmcracker-architecture.svg`

**Interfaces:**
- Consumes: `steps`, `comparison`.
- Produces: `HowItWorks.astro` (propless) rendering `<ol>` of ordered steps with `<h2>`, plus an `<img src="/swarmcracker-architecture.svg" alt="…" width="…" height="…">` (dimensions set to prevent layout shift). `Comparison.astro` (propless) renders a three-column comparison `<table>` with a `<caption>` and `<th scope="col">` headers; on narrow screens the table is wrapped in a horizontally scrollable container rather than reflowed into an ambiguous card stack.

- [ ] **Step 1: Add assertions and confirm failure**

Append to `REQUIRED_TEXT`: the first step title and the comparison caption. Append `'/swarmcracker-architecture.svg'` to `REQUIRED_LINKS` (matched by substring in the rendered `<img src>`).

Run: `cd website && npm run verify`
Expected: FAIL.

- [ ] **Step 2: Implement both components and copy the asset**

Comparison copy must be even-handed: no strawman claims about Docker or Kubernetes.

- [ ] **Step 3: Update `index.astro` and verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 4: Commit**

```bash
git add website docs/architecture/swarmcracker-architecture.svg
git commit -m "feat(website): add how-it-works and honest comparison sections"
```

---

### Task 7: Quickstart with copy-to-clipboard

**Files:**
- Create: `website/src/components/Quickstart.astro`
- Create: `website/src/scripts/copy.ts`
- Modify: `website/src/layouts/BaseLayout.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `quickstart`.
- Produces: `copy.ts` exporting `function initCopy(): void`. `Quickstart.astro` (propless) renders a `<section id="quickstart">` with an `<h2>`, one `<figure>` per command containing a `<pre><code id="cmd-N">…</code></pre>` and a `<button type="button" data-copy-target="#cmd-N" aria-label="Copy: <label>">`. `BaseLayout` loads the script once.

- [ ] **Step 1: Add JS-disabled and copy-mapping assertions and confirm failure**

Append to `REQUIRED_TEXT`: the full text of the first and last quickstart command (proving they are server-rendered and survive JS-disabled, Review Focus 3). Add to `runChecks` a loop asserting every `data-copy-target="#id"` has a matching `id="id"` (already implemented in Task 1; it now becomes exercised, Review Focus 4).

Run: `cd website && npm run verify`
Expected: FAIL listing the missing commands.

- [ ] **Step 2: Implement `Quickstart.astro` and `copy.ts`**

`initCopy()` queries `[data-copy-target]`, and on click writes `document.querySelector(target).textContent` to `navigator.clipboard`, then sets a `data-copied` attribute and updates an adjacent `aria-live="polite"` status element to "Copied". Wrap clipboard access in `try/catch`; on failure, select the code text instead. Guard for `navigator.clipboard === undefined`.

- [ ] **Step 3: Hide copy buttons without JS**

Style `.copy-btn { display: none }` and `html.js .copy-btn { display: inline-flex }`, relying on the `js` class from Task 1. This keeps the control from appearing dead when JavaScript is off (Review Focus 3).

- [ ] **Step 4: Give code blocks narrow-width overflow handling**

Give `pre`/`code` blocks `overflow-x: auto` and `max-width: 100%` so the long `curl … | sudo bash` line scrolls inside its own block (Review Focus 1). Verify manually at 360 px and 768 px that the page itself never scrolls horizontally and each copy button sits with its own command (Review Focus 4).

- [ ] **Step 5: Verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 6: Commit**

```bash
git add website
git commit -m "feat(website): add quickstart with progressive-enhancement copy buttons"
```

---

### Task 8: Security and docs gateway

**Files:**
- Create: `website/src/components/Security.astro`
- Create: `website/src/components/DocsGateway.astro`
- Modify: `website/src/pages/index.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: `security`, `docsCards`, `links`.
- Produces: `Security.astro` (propless) rendering the security points as a list with an `<h2>`. `DocsGateway.astro` (propless) rendering one `<a class="docs-card">` per `docsCard`, with the card `<h3>` inside the anchor and `hreflang` omitted.

- [ ] **Step 1: Add assertions and confirm failure**

Append to `REQUIRED_TEXT`: the first security point title and the first docs card title. Append each `docsCards[].href` to `REQUIRED_LINKS`.

Run: `cd website && npm run verify`
Expected: FAIL.

- [ ] **Step 2: Implement both components**

Docs card hrefs must all begin `https://docs.swarmcracker.com` (Global Constraints). Cards are a responsive grid that becomes a single column on mobile.

- [ ] **Step 3: Update `index.astro` and verify**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.

- [ ] **Step 4: Commit**

```bash
git add website
git commit -m "feat(website): add security and docs gateway sections"
```

---

### Task 9: Brand assets, accessibility polish, and full verification

**Files:**
- Create: `website/public/favicon.svg`
- Create: `website/public/og-image.svg`
- Create: `website/public/sitemap.xml` (hand-written, listing `/`)
- Modify: `website/src/layouts/BaseLayout.astro`
- Modify: `website/scripts/verify-build.mjs`

**Interfaces:**
- Consumes: everything built so far.
- Produces: `BaseLayout` referencing `/favicon.svg` and `/og-image.svg`; a root-level ruleset for `:focus-visible`, `.skip-link`, and a global `prefers-reduced-motion` reset.

- [ ] **Step 1: Add asset and a11y assertions and confirm failure**

Append to `REQUIRED_TEXT`: `'rel="icon" href="/favicon.svg"'`, `'property="og:image" content="https://swarmcracker.com/og-image.svg"'`, `'class="skip-link"'`, and `'id="main"'`.

Run: `cd website && npm run verify`
Expected: FAIL.

- [ ] **Step 2: Create favicon, OG image, and sitemap**

`favicon.svg` is a small orange-on-dark mark; `og-image.svg` is a 1200×630 card using `site.title` and the orange accent, authored by hand (no build-time generation, per the spec decision). `sitemap.xml` lists `https://swarmcracker.com/`.

- [ ] **Step 3: Wire assets and global a11y styles**

Add the icon + OG image tags in `BaseLayout`. Add `:focus-visible { outline: 2px solid var(--sc-orange); outline-offset: 2px }`, a visually-hidden `.skip-link` that becomes visible on focus, and confirm the global `prefers-reduced-motion` reset from Task 1 is in the built CSS.

- [ ] **Step 4: Verify contrast (Review Focus 5)**

Check the token pairs against WCAG AA: `--sc-text` on `--sc-bg`, `--sc-text-muted` on `--sc-surface`, and accent `#FF6B35` used as text (not just as a background). If accent-as-text fails AA, use `--sc-orange-light` or a lighter tint for text use and leave `#FF6B35` for backgrounds/borders. Record the chosen pairs in a comment above the tokens in `tokens.css`.

- [ ] **Step 5: Full verification pass**

Run: `cd website && npm run verify`
Expected: `verify-build OK`.
Run a production preview: `npm run build && npm run preview`.
Then, against the preview URL, run Lighthouse (desktop and mobile) if Chrome is available: `npx --yes lighthouse http://localhost:4321 --only-categories=performance,accessibility,best-practices,seo --view --chrome-flags="--headless --no-sandbox"`. Expected: all four ≥ 95. If Chrome is unavailable in this environment, record that Lighthouse must be run manually before deploy.
Manual checks: JS disabled (content + links intact, no dead copy buttons), keyboard tab order, screen-reader landmarks, and widths 360 / 768 / 1280 px.

- [ ] **Step 6: Commit**

```bash
git add website
git commit -m "feat(website): add brand assets, sitemap, and accessibility polish"
```

---

### Task 10: Deploy to Cloudflare Pages and relocate docs

**Files:**
- Create: `website/README.md`
- Modify: `mkdocs.yml`
- Modify: `docs/CNAME`
- Modify: `README.md`

**Interfaces:**
- Consumes: the built site from Task 9.
- Produces: documented deployment, an updated docs hostname, and a README that links to `docs.swarmcracker.com`.

- [ ] **Step 1: Document the deployment**

Write `website/README.md` describing local dev (`npm install`, `npm run dev`), verification (`npm run verify`), and the Cloudflare Pages setup: connect the repository, set **root directory** `website`, **build command** `npm run build`, **output directory** `dist`, Node 20+, and add the custom domain `swarmcracker.com` plus a redirect from `www` to the apex.

- [ ] **Step 2: Move the docs hostname**

Change `docs/CNAME` to `docs.swarmcracker.com`. Change `mkdocs.yml` `site_url` to `https://docs.swarmcracker.com/`. In the DNS provider, point `docs` at GitHub Pages (per GitHub's custom-domain instructions) and add a redirect from `swarmcracker.restuhaqza.dev` to `docs.swarmcracker.com` if the old host is retained.

- [ ] **Step 3: Update the root README**

Replace the `swarmcracker.restuhaqza.dev` documentation link with `https://docs.swarmcracker.com`, and add a line pointing to the landing page at `https://swarmcracker.com`.

- [ ] **Step 4: Verify the deployment**

Deploy the `website/` directory to a Cloudflare Pages project and confirm the `*.pages.dev` preview loads. Then, after DNS is configured:
- `curl -sI https://swarmcracker.com | head -1` → `HTTP/2 200`
- `curl -sI https://www.swarmcracker.com | head -1` → a 3xx redirect to the apex
- `curl -sI https://docs.swarmcracker.com | head -1` → `HTTP/2 200`
- Open `swarmcracker.com` on desktop and mobile and confirm the install CTA and docs link both work end to end.

- [ ] **Step 5: Commit**

```bash
git add website/README.md mkdocs.yml docs/CNAME README.md
git commit -m "chore: deploy landing page and move docs to docs.swarmcracker.com"
```

---

## Self-Review

**Spec coverage:** Every spec section maps to a task — goals/success criteria → Tasks 4, 9, 10; page structure → Tasks 3–8; visual direction → Tasks 1, 4, 5, 9; technical architecture → Tasks 1–2, 9; accessibility/performance → Tasks 7, 9; deployment/docs relocation → Task 10; rollout steps → Tasks 9–10.

**Review Focus coverage:** (1) narrow-width overflow → Task 4 Step 2, Task 7 Step 4; (2) reduced motion → Task 4 Step 2 + harness check; (3) JS disabled → Task 7 Steps 1, 3; (4) 768 px copy mapping → Task 7 Steps 1, 4; (5) contrast → Task 9 Step 4.

**Type consistency:** `site.ts` interfaces defined in Task 2 are the only ones consumed by later components; `runChecks(html, css)` and the two arrays are stable from Task 1; `initCopy()` is the only exported script symbol.
