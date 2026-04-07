# SwarmCracker Landing Page - React + Vite

Modern landing page built with React, Vite, and Tailwind CSS.

## Tech Stack

- **React 18** - UI framework
- **Vite** - Build tool and dev server
- **Tailwind CSS** - Utility-first CSS framework
- **GitHub Pages** - Hosting and deployment

## Development

### Prerequisites

- Node.js 20+
- npm or yarn

### Install Dependencies

```bash
cd docs/site
npm install
```

### Development Server

```bash
npm run dev
```

Open http://localhost:5173 in your browser.

### Build for Production

```bash
npm run build
```

Output will be in `dist/` directory.

### Preview Production Build

```bash
npm run preview
```

## Project Structure

```
docs/site/
├── src/
│   ├── main.jsx          # React entry point
│   ├── App.jsx           # Main app component
│   └── index.css         # Global styles + Tailwind
├── index.html            # HTML entry point
├── package.json          # Dependencies
├── vite.config.js        # Vite configuration
├── tailwind.config.js    # Tailwind configuration
├── postcss.config.js     # PostCSS configuration
└── README.md             # This file
```

## Components

The landing page consists of these sections (all in `App.jsx`):

1. **Navigation** - Fixed header with logo and links
2. **Hero** - Tagline, description, and quick start code
3. **Features** - 6 feature cards with icons
4. **Stats** - Performance metrics
5. **How It Works** - 4-step process
6. **Installation** - Tabbed code blocks (Manager/Worker/Manual)
7. **CTA** - Call-to-action
8. **Footer** - Links and resources

## Customization

### Colors

Edit `tailwind.config.js`:

```js
colors: {
  primary: {
    DEFAULT: '#FF6B35',
    dark: '#E55A2B',
    light: '#FF8E53',
  },
  // ...
}
```

### Content

Edit `src/App.jsx`:
- `features` array - Feature cards
- `stats` array - Statistics
- `steps` array - How it works steps
- `installCommands` object - Installation code snippets

### Fonts

Edit `index.html` to change Google Fonts imports.

## Deployment

### Automatic (GitHub Actions)

The site auto-deploys to GitHub Pages when you push to `main`:

```bash
git add .
git commit -m "docs: update landing page"
git push origin main
```

Workflow: `.github/workflows/pages.yml`

### Manual

```bash
npm run build
# Upload dist/ to hosting
```

## Custom Domain

The site is configured for: **https://swarmcracker.restuhaqza.dev**

The CNAME file is in the root of `docs/site/`.

## Features

- ✅ Responsive design (mobile/tablet/desktop)
- ✅ Dark theme with orange accents
- ✅ Syntax-highlighted code blocks
- ✅ Copy-to-clipboard functionality
- ✅ Smooth scroll navigation
- ✅ Tabbed installation sections
- ✅ Interactive hover effects
- ✅ SEO optimized meta tags
- ✅ Fast build times with Vite
- ✅ Production-optimized build

## License

Apache 2.0 (same as SwarmCracker)
