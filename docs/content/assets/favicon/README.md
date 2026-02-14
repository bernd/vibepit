# Favicon Workflow

This folder is generated from one source script:

- Script: `docs/scripts/generate_favicons.py`

Do not hand-edit files in this folder unless you are doing a one-off experiment.
For real changes, update the script and regenerate.

## Regenerate

From the repository root:

```bash
python3 docs/scripts/generate_favicons.py
UV_CACHE_DIR=.uv-cache uv run --project docs mkdocs build --strict
```

## Generated Files

- `favicon.svg`
- `favicon.ico`
- `favicon-16x16.png`
- `favicon-32x32.png`
- `favicon-48x48.png`
- `apple-touch-icon.png`
- `android-chrome-192x192.png`
- `android-chrome-512x512.png`
- `safari-pinned-tab.svg`
- `site.webmanifest`

## Wiring

Favicon links are injected via:

- `docs/overrides/main.html`
- `mkdocs.yml` (`theme.custom_dir`, `theme.favicon`)
