# Trove Website

Static open-source showcase site for Trove.

## Preview locally

```sh
cd website
python3 -m http.server 4177
```

Open <http://localhost:4177>.

## Files

- `index.html` — landing page content
- `styles.css` — all styling
- `assets/trove-mark.svg` — simple project mark/favicon
- `assets/trove-dashboard-hero.png` — cropped live dashboard screenshot for hero sections
- `assets/trove-dashboard.png` — full live dashboard screenshot

## Publish options

The site is plain static HTML/CSS, so it can be served from GitHub Pages, Cloudflare Pages, Netlify, nginx, Caddy, or any static file host.

For GitHub Pages, set the Pages source to this `website/` folder if using a Pages workflow, or copy these files to a `gh-pages` branch.
