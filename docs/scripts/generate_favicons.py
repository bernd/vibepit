#!/usr/bin/env python3
"""Generate Vibepit favicon assets.

All favicon raster variants are resized from the VP monogram artwork.
"""

from __future__ import annotations

import json
import struct
import zlib
from pathlib import Path

from PIL import Image


ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "docs" / "content" / "assets" / "favicon"
VP_MONOGRAM = ROOT / "assets" / "logo-vp.png"


def png_chunk(tag: bytes, data: bytes) -> bytes:
    return (
        struct.pack(">I", len(data))
        + tag
        + data
        + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
    )


def encode_png(width: int, height: int, rgba: bytes) -> bytes:
    stride = width * 4
    rows = []
    for y in range(height):
        row = rgba[y * stride : (y + 1) * stride]
        rows.append(b"\x00" + row)
    raw = b"".join(rows)

    signature = b"\x89PNG\r\n\x1a\n"
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    idat = zlib.compress(raw, 9)
    return signature + png_chunk(b"IHDR", ihdr) + png_chunk(b"IDAT", idat) + png_chunk(b"IEND", b"")


def write_logo_png(path: Path, logo: Image.Image, size: int) -> bytes:
    resized = logo.resize((size, size), Image.Resampling.LANCZOS)
    png = encode_png(size, size, resized.tobytes())
    path.write_bytes(png)
    return png


def normalize_monogram(source: Image.Image, padding_ratio: float = 0.08) -> Image.Image:
    rgba = source.convert("RGBA")
    bbox = rgba.getbbox()
    if bbox is not None:
        rgba = rgba.crop(bbox)

    side = max(rgba.width, rgba.height)
    pad = max(1, int(side * padding_ratio))
    canvas_side = side + pad * 2
    canvas = Image.new("RGBA", (canvas_side, canvas_side), (0, 0, 0, 0))
    ox = (canvas_side - rgba.width) // 2
    oy = (canvas_side - rgba.height) // 2
    canvas.paste(rgba, (ox, oy), rgba)
    return canvas


def write_ico(path: Path, images: list[tuple[int, bytes]]) -> None:
    header = struct.pack("<HHH", 0, 1, len(images))
    entries = []
    offset = 6 + 16 * len(images)
    payload = []

    for size, png in images:
        w = 0 if size >= 256 else size
        h = 0 if size >= 256 else size
        entry = struct.pack(
            "<BBBBHHII",
            w,
            h,
            0,
            0,
            1,
            32,
            len(png),
            offset,
        )
        entries.append(entry)
        payload.append(png)
        offset += len(png)

    path.write_bytes(header + b"".join(entries) + b"".join(payload))


def write_manifest(path: Path) -> None:
    manifest = {
        "name": "Vibepit",
        "short_name": "Vibepit",
        "icons": [
            {
                "src": "./android-chrome-192x192.png",
                "sizes": "192x192",
                "type": "image/png",
            },
            {
                "src": "./android-chrome-512x512.png",
                "sizes": "512x512",
                "type": "image/png",
            },
        ],
        "theme_color": "#0d1829",
        "background_color": "#0d1829",
        "display": "standalone",
    }
    path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    with Image.open(VP_MONOGRAM) as src:
        monogram = normalize_monogram(src)

    png16 = write_logo_png(OUT / "favicon-16x16.png", monogram, 16)
    png32 = write_logo_png(OUT / "favicon-32x32.png", monogram, 32)
    png48 = write_logo_png(OUT / "favicon-48x48.png", monogram, 48)
    write_logo_png(OUT / "apple-touch-icon.png", monogram, 180)
    write_logo_png(OUT / "android-chrome-192x192.png", monogram, 192)
    write_logo_png(OUT / "android-chrome-512x512.png", monogram, 512)

    write_ico(OUT / "favicon.ico", [(16, png16), (32, png32), (48, png48)])
    write_manifest(OUT / "site.webmanifest")


if __name__ == "__main__":
    main()
