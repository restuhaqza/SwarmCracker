# SwarmCracker Landing Page

The marketing landing page for [swarmcracker.com](https://swarmcracker.com), built with [Astro](https://astro.build/).

## Local Development

```bash
# Install dependencies
npm install

# Start the dev server (http://localhost:4321)
npm run dev
```

## Verification

Run the full verification suite (type-check, build, and automated checks):

```bash
npm run verify
```

This runs `astro check`, `astro build`, and `node scripts/verify-build.mjs` to confirm the site builds cleanly and all automated checks pass.

## Deployment (Cloudflare Pages)

The site is deployed via [Cloudflare Pages](https://pages.cloudflare.com/). To set it up:

1. **Connect the repository** — In the Cloudflare dashboard, go to **Workers & Pages → Create → Pages → Connect to Git** and select the SwarmCracker repository.
2. **Configure the build:**
   - **Root directory:** `website`
   - **Build command:** `npm run build`
   - **Output directory:** `dist`
   - **Node version:** 20+ (set via `NODE_VERSION` environment variable in the Pages project settings)
3. **Add the custom domain:**
   - In **Pages → Custom domains**, add `swarmcracker.com`.
   - Cloudflare will prompt you to add the required DNS records if they don't already exist.
4. **Set up the `www` redirect:**
   - Recommended: create a **Redirect Rule** in the Cloudflare dashboard (**Rules → Redirect Rules**) that 301-redirects `www.swarmcracker.com` to `https://swarmcracker.com` (e.g. `concat("https://swarmcracker.com", http.request.uri.path)` with *Preserve query string* enabled).
   - Alternatively, add `www.swarmcracker.com` as a second **custom domain** on the Pages project (**Pages → Custom domains → Set up a custom domain**); Cloudflare Pages 301-redirects secondary custom domains to the primary domain automatically.
   - Note: a proxied `CNAME` for `www` pointing at the apex only mirrors the apex content (HTTP 200) — it does **not** redirect — so it is not sufficient on its own for the `www` → apex redirect.

## Manual Verification Steps

After deployment and DNS configuration, verify the following (these require account/DNS access and cannot be automated in CI):

- [ ] `curl -sI https://swarmcracker.com | head -1` → `HTTP/2 200`
- [ ] `curl -sI https://www.swarmcracker.com | head -1` → a 3xx redirect to the apex
- [ ] `curl -sI https://docs.swarmcracker.com | head -1` → `HTTP/2 200`
- [ ] Open `swarmcracker.com` on desktop and mobile and confirm the install CTA and docs link both work end to end.
- [ ] Confirm the `*.pages.dev` preview URL loads correctly before DNS propagation.
