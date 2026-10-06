# SwarmCracker Landing Page — Design Spec

**Date:** 2026-10-06
**Status:** Approved (design), pending spec review
**Owner:** Restu Muzakir
**Domain:** swarmcracker.com

## 1. Purpose

Build a public landing page at `swarmcracker.com` that presents SwarmCracker,
communicates its core value proposition, and funnels visitors to install it or
read the documentation.

### Goals

- A visitor understands "Docker-Swarm-style orchestration, but workloads run in
  hardware-isolated Firecracker microVMs" in under 10 seconds.
- The visitor can reach either an install command or the documentation in one
  scroll or one click.
- The page is fast, accessible, and works without JavaScript.

### Success criteria

- Install CTA and Docs link both visible above/one-scroll from the fold.
- Lighthouse (desktop and mobile) ≥ 95 for performance, accessibility, SEO, and
  best practices.
- Fully usable with JavaScript disabled (core content and links).
- Mobile-first layout verified at 360 px, 768 px, and 1280 px+.
- All outbound links (docs, GitHub, releases) resolve.

### Non-goals

- Replacing or rewriting the existing MkDocs documentation.
- Adding a blog, changelog page, or marketing automation.
- Building any application functionality. This is a single static page.

## 2. Audience

- Developers evaluating container/VM isolation for self-hosted workloads.
- Existing Docker Swarm users curious about stronger isolation.
- People arriving from GitHub, release announcements, or blog posts.

## 3. Relationship to the existing docs site

- `swarmcracker.com` — the new landing page.
- `docs.swarmcracker.com` — the existing MkDocs Material site, relocated from
  `swarmcracker.restuhaqza.dev`. Content is unchanged; only the hostname moves.
- The landing page links out to `docs.swarmcracker.com`; it does not embed or
  duplicate docs.
- A redirect from the old docs hostname to the new one is desirable but optional
  and out of scope for the first release.

## 4. Page structure

Single scrolling page with anchor navigation.

1. **Navigation bar** — SwarmCracker wordmark, links to Docs, GitHub
   (with live star count), and a "Get Started" button.
2. **Hero** — headline, subhead, dual CTA (**Install** / **Read the docs**),
   and a terminal visual showing `swarmcracker service create` resolving to
   microVMs spinning up.
3. **Stat strip** — key credibility numbers: 87% test coverage, 70/70 E2E tests,
   47 security/code-review items addressed, Apache-2.0 license, ~100 ms boot.
4. **Why SwarmCracker** — 4–6 value cards: per-VM kernel, SwarmKit compatible,
   KVM hardware isolation, ~100 ms boot, VXLAN cross-node networking, rolling
   updates.
5. **How it works** — the manager → agent → executor → Firecracker flow
   (including Consul/VXLAN) as 3–4 concise steps, using the existing
   architecture diagram asset.
6. **Quickstart** — the exact install, setup, cluster, and deploy commands from
   the README, each with copy-to-clipboard.
7. **When to use it** — a short, honest comparison against plain Docker and
   against Kubernetes. No strawman claims.
8. **Security** — jailer, seccomp filtering, KVM isolation, hardened builds.
9. **Docs gateway** — cards linking to Getting Started, CLI Reference,
   Networking, and Security in the docs site.
10. **Footer** — license, GitHub, releases, and contact.

## 5. Visual direction

- **Dark-first**, modern developer-tool aesthetic.
- Base: near-black/charcoal. Accent: existing brand orange `#FF6B35` (with
  `#FF8E53` / `#E55A2B`) used as glow and gradient.
- Typography: Inter (text) and JetBrains Mono (code), matching the docs site.
- Terminal-forward hero with a subtle grid/scanline texture.
- Motion implemented in CSS only and gated behind
  `prefers-reduced-motion: no-preference`.
- Light mode is explicitly deferred; the dark theme is the only first-release
  theme.
- Brand continuity with the existing docs site is required, so tokens
  (`--swarmcracker-*`) are reused rather than re-invented.

## 6. Technical architecture

- **Framework:** Astro 5, `output: 'static'`. No framework islands in the first
  release; only minimal vanilla JS for mobile navigation and copy-to-clipboard.
- **Styling:** scoped CSS within Astro components plus a shared token file.
  No Tailwind (keeps the dependency footprint small and matches the project's
  minimal-dependency ethos). Revisit only if velocity becomes a problem.
- **Content source:** headline stats, commands, feature copy, and docs links
  live in a single `website/src/data/site.ts` so copy is edited in one place.
- **Assets:** reuse `docs/architecture/swarmcracker-architecture.svg`; add OG
  image and favicon under `website/public/`.
- **Repo layout:**

  ```
  website/
    astro.config.mjs
    package.json
    tsconfig.json
    public/            # favicon, og-image, robots.txt, architecture svg
    src/
      data/site.ts     # single source of truth for copy/links/stats
      styles/tokens.css
      components/      # Nav, Hero, Terminal, StatStrip, FeatureGrid,
                       # HowItWorks, Quickstart, Comparison, Security,
                       # DocsGateway, Footer
      layouts/BaseLayout.astro
      pages/index.astro
  ```

### Deployment

- **Cloudflare Pages** project sourced from this repository.
- Root directory: `website`. Build command: `npm run build`. Output: `dist`.
- Custom domain: `swarmcracker.com` (apex), with `www` redirected to the apex.
- The docs site is deployed/served at `docs.swarmcracker.com` via CNAME to the
  existing MkDocs host. Migrating the docs build to Cloudflare Pages is out of
  scope for this spec and can be a follow-up.

## 7. Accessibility and performance

- Semantic landmarks (`header`, `main`, `nav`, `footer`), one `h1`, logical
  heading order.
- All interactive elements keyboard reachable with visible focus states.
- Sufficient color contrast for text on the dark background (WCAG AA).
- No layout shift: reserve space for the terminal and images.
- Ship minimal JS; defer non-critical scripts. Prefer system font fallbacks to
  avoid blocking text render.

## 8. Testing and verification

- `astro check` passes and `astro build` completes with no errors.
- Lighthouse (desktop + mobile) ≥ 95 across the four categories.
- JS-disabled render check: content and links still present.
- Keyboard and screen-reader smoke test.
- Link check against `docs.swarmcracker.com`, GitHub, and releases.
- Visual check at 360 / 768 / 1280 px widths.

## 9. Rollout

1. Implement the Astro site under `website/` and verify locally.
2. Create the Cloudflare Pages project from the repo and confirm the
   `*.pages.dev` preview.
3. Point `swarmcracker.com` (and `www`) at the Pages project.
4. Relocate the docs site to `docs.swarmcracker.com` and update the docs link in
   the landing page and `README.md`.
5. Update `README.md` documentation link to the new docs hostname.

## 10. Decisions and open questions

### Decided

- Landing page and docs are separate sites; docs move to a subdomain.
- Astro 5, static output, minimal JavaScript.
- Cloudflare Pages hosting, source in-repo under `website/`.
- Dark-first visual direction; light mode deferred.
- Hero uses a terminal visual.

### Open (resolved at implementation time, no spec change needed)

- Whether the hero terminal is animated or static. Decision: implement the
  animated version, but ensure the terminal's final content is meaningful and
  readable without animation, and degrade to a static terminal under
  `prefers-reduced-motion`. This keeps the accessibility and JS-disabled
  guarantees intact.
- Exact OG image treatment. Decision: derive from the hero at build time using a
  static SVG/PNG committed to `website/public/`.
