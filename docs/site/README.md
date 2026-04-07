# SwarmCracker Landing Page

This directory contains the SwarmCracker landing page deployed to GitHub Pages.

## Structure

```
docs/site/
├── index.html          # Main landing page
└── .nojekyll          # Disable Jekyll processing
```

## Deployment

The site is automatically deployed to GitHub Pages when changes are pushed to `main` branch.

**Workflow**: `.github/workflows/pages.yml`

**URL**: https://restuhaqza.github.io/SwarmCracker/

## Local Testing

Test the page locally before pushing:

```bash
# Using Python's built-in server
cd docs/site
python3 -m http.server 8000

# Open in browser
# http://localhost:8000
```

## Customization

### Colors

Edit CSS variables in `index.html`:

```css
:root {
    --primary: #FF6B35;        /* Orange accent */
    --secondary: #1E3A5F;      /* Dark blue */
    --accent: #00D4AA;         /* Teal */
    --bg-dark: #0A0E1A;        /* Background */
}
```

### Content

- **Hero section**: Update tagline and description
- **Features**: Edit feature cards in the features grid
- **Installation**: Update commands in install tabs
- **Footer**: Modify links and copyright

### Adding Pages

1. Create new HTML file in `docs/site/`
2. Add navigation link in header
3. Update workflow if needed

## Assets

### Images

Place images in `docs/site/images/` and reference with relative paths:

```html
<img src="images/feature.png" alt="Feature">
```

### Icons

Using emoji icons for simplicity. Can replace with:
- Font Awesome
- Heroicons
- Custom SVG

## SEO

Update meta tags in `<head>`:

```html
<meta name="description" content="Your description">
<meta property="og:title" content="Your title">
<meta property="og:description" content="Your description">
```

## Analytics

Add analytics tracking code before `</head>`:

```html
<!-- Google Analytics -->
<script async src="https://www.googletagmanager.com/gtag/js?id=GA_MEASUREMENT_ID"></script>
```

## Troubleshooting

### Site not updating

1. Check workflow status: https://github.com/restuhaqza/SwarmCracker/actions
2. Verify `docs/site/` path in workflow
3. Check GitHub Pages settings in repository

### 404 errors

- Ensure `.nojekyll` file exists
- Check file paths are relative
- Verify GitHub Pages is enabled in repository settings

## GitHub Pages Settings

Enable in repository:
1. Settings → Pages
2. Source: GitHub Actions
3. Custom domain (optional)

## License

Same as SwarmCracker (Apache 2.0)
