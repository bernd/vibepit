# Favicon Workflow

This folder is generated from one source script:

- Script: `docs/scripts/generate_favicons.py`
- Monogram source: `assets/logo-vp.png`

Do not hand-edit files in this folder unless you are doing a one-off experiment.
For real changes, update the script and regenerate.

All favicon variants are generated from `assets/logo-vp.png`.
SVG favicon variants are intentionally not used.

## Regenerate

From the repository root:

```bash
python3 docs/scripts/generate_favicons.py
UV_CACHE_DIR=.uv-cache uv run --project docs mkdocs build --strict
```

## Generated Files

- `favicon.ico`
- `favicon-16x16.png`
- `favicon-32x32.png`
- `favicon-48x48.png`
- `apple-touch-icon.png`
- `android-chrome-192x192.png`
- `android-chrome-512x512.png`
- `site.webmanifest`

## Wiring

Favicon links are injected via:

- `docs/overrides/main.html`
- `mkdocs.yml` (`theme.custom_dir`, `theme.favicon`)
