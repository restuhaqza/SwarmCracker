# SwarmCracker Docs

The documentation site at **https://docs.swarmcracker.com**, built with
[Astro](https://astro.build) + [Starlight](https://starlight.astro.build).

This replaced the previous MkDocs Material site. Content here is the source of
truth for published docs.

## Commands

```bash
npm install        # once
npm run dev        # dev server with hot reload (http://localhost:4321)
npm run build      # production build -> dist/
npm run preview    # serve the built site (includes Pagefind search)
npm run check      # astro type/content check
npm run verify     # check + internal link check + build
npm run deploy     # build + wrangler pages deploy dist
```

> Search is powered by Pagefind and is generated **at build time**, so it only
> works in `preview`/`build`, not in `dev`.

## Content layout

Published pages live in `src/content/docs/` and map 1:1 to URLs:

```
src/content/docs/
├── index.mdx                     /                     (splash home)
├── getting-started/index.md      /getting-started/
├── guides/                       /guides/…             (configuration, networking,
│                                                       security, snapshots, …)
├── reference/                    /reference/…          (cli, api)
├── architecture/                 /architecture/…       (index, swarmkit)
└── contributing/                 /contributing/…       (guidelines, security,
                                                        testing/, reference/)
```

Each page needs YAML frontmatter with at least a `title`. The sidebar is defined
in `astro.config.mjs`; groups use `autogenerate` so new files appear
automatically.

## Editing

- **Branding/theme** — `src/styles/custom.css` maps the landing-page brand
  (Big Shoulders Display / Archivo / B612 Mono, safety orange) onto Starlight's
  design tokens. Fonts are self-hosted in `public/fonts/`.
- **Logo/favicon** — `src/assets/logo.svg`, `public/favicon.svg`.
- **Redirects** — `public/_redirects` maps old MkDocs URLs (`/user/...`,
  `/dev/...`) to the new IA on Cloudflare Pages.
- **Link checking** — `scripts/check-links.mjs` validates every internal link
  against the content tree; run it via `npm run verify` (also run in CI).

## Deployment

`.github/workflows/docs.yml` builds this directory and deploys `dist/` to the
Cloudflare Pages project `swarmcracker-docs` on every push to `main` that
touches `docs-site/`.

Required repository configuration (Settings → Secrets and variables → Actions):

- Secret `CLOUDFLARE_API_TOKEN` with the "Cloudflare Pages: Edit" permission,
  scoped to the Cloudflare account that owns the `swarmcracker-docs` project.
  The account is inferred from the token, so no account identifier is stored in
  the workflow.
