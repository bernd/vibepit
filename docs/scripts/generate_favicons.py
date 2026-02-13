#!/usr/bin/env python3
"""Generate Vibepit shield-V favicon assets without third-party dependencies."""

from __future__ import annotations

import json
import math
import struct
import zlib
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "docs" / "content" / "assets" / "favicon"


def clamp(v: float, lo: float, hi: float) -> float:
    return lo if v < lo else hi if v > hi else v


def dist_to_segment(px: float, py: float, ax: float, ay: float, bx: float, by: float) -> float:
    abx = bx - ax
    aby = by - ay
    apx = px - ax
    apy = py - ay
    ab2 = abx * abx + aby * aby
    if ab2 == 0:
        dx = px - ax
        dy = py - ay
        return math.sqrt(dx * dx + dy * dy)
    t = clamp((apx * abx + apy * aby) / ab2, 0.0, 1.0)
    cx = ax + t * abx
    cy = ay + t * aby
    dx = px - cx
    dy = py - cy
    return math.sqrt(dx * dx + dy * dy)


def mix(c1: tuple[int, int, int], c2: tuple[int, int, int], t: float) -> tuple[float, float, float]:
    return (
        c1[0] + (c2[0] - c1[0]) * t,
        c1[1] + (c2[1] - c1[1]) * t,
        c1[2] + (c2[2] - c1[2]) * t,
    )


def blend(
    base: tuple[float, float, float, float],
    top_rgb: tuple[float, float, float],
    top_a: float,
) -> tuple[float, float, float, float]:
    br, bg, bb, ba = base
    out_a = top_a + ba * (1.0 - top_a)
    if out_a <= 0:
        return (0.0, 0.0, 0.0, 0.0)
    out_r = (top_rgb[0] * top_a + br * ba * (1.0 - top_a)) / out_a
    out_g = (top_rgb[1] * top_a + bg * ba * (1.0 - top_a)) / out_a
    out_b = (top_rgb[2] * top_a + bb * ba * (1.0 - top_a)) / out_a
    return (out_r, out_g, out_b, out_a)


def shape_v(x: float, y: float, width: float = 10.5) -> bool:
    d1 = dist_to_segment(x, y, 34.0, 30.0, 50.0, 72.0)
    d2 = dist_to_segment(x, y, 50.0, 72.0, 66.0, 30.0)
    return min(d1, d2) <= width * 0.5


def solve_quad_t_for_y(y: float, y0: float, y1: float, y2: float) -> float:
    a = y0 - 2.0 * y1 + y2
    b = -2.0 * y0 + 2.0 * y1
    c = y0 - y
    if abs(a) < 1e-9:
        if abs(b) < 1e-9:
            return 0.0
        return clamp(-c / b, 0.0, 1.0)
    disc = b * b - 4.0 * a * c
    if disc < 0.0:
        disc = 0.0
    sd = math.sqrt(disc)
    t1 = (-b + sd) / (2.0 * a)
    t2 = (-b - sd) / (2.0 * a)
    if 0.0 <= t1 <= 1.0:
        return t1
    if 0.0 <= t2 <= 1.0:
        return t2
    return clamp(t1, 0.0, 1.0)


def quad_x_at_y(
    y: float,
    p0: tuple[float, float],
    p1: tuple[float, float],
    p2: tuple[float, float],
) -> float:
    t = solve_quad_t_for_y(y, p0[1], p1[1], p2[1])
    mt = 1.0 - t
    return mt * mt * p0[0] + 2.0 * mt * t * p1[0] + t * t * p2[0]


def shield_shape(
    x: float,
    y: float,
    *,
    top_y: float,
    side_top_y: float,
    side_bottom_y: float,
    mid_y: float,
    bottom_y: float,
    left_side_x: float,
    right_side_x: float,
    top_left_x: float,
    top_right_x: float,
    mid_left_x: float,
    mid_right_x: float,
    bottom_left_x: float,
    bottom_right_x: float,
    low_ctrl_left: tuple[float, float],
    low_ctrl_right: tuple[float, float],
    deep_ctrl_left: tuple[float, float],
    deep_ctrl_right: tuple[float, float],
) -> bool:
    if y < top_y or y > bottom_y:
        return False

    if y < side_top_y:
        xl = quad_x_at_y(y, (left_side_x, side_top_y), (left_side_x, top_y), (top_left_x, top_y))
        xr = quad_x_at_y(y, (right_side_x, side_top_y), (right_side_x, top_y), (top_right_x, top_y))
    elif y <= side_bottom_y:
        xl = left_side_x
        xr = right_side_x
    elif y <= mid_y:
        xl = quad_x_at_y(y, (left_side_x, side_bottom_y), low_ctrl_left, (mid_left_x, mid_y))
        xr = quad_x_at_y(y, (right_side_x, side_bottom_y), low_ctrl_right, (mid_right_x, mid_y))
    else:
        xl = quad_x_at_y(y, (mid_left_x, mid_y), deep_ctrl_left, (bottom_left_x, bottom_y))
        xr = quad_x_at_y(y, (mid_right_x, mid_y), deep_ctrl_right, (bottom_right_x, bottom_y))

    return xl <= x <= xr


def shield_outer(x: float, y: float) -> bool:
    return shield_shape(
        x,
        y,
        top_y=8.0,
        side_top_y=22.0,
        side_bottom_y=60.0,
        mid_y=76.0,
        bottom_y=90.0,
        left_side_x=14.0,
        right_side_x=86.0,
        top_left_x=28.0,
        top_right_x=72.0,
        mid_left_x=24.0,
        mid_right_x=76.0,
        bottom_left_x=44.0,
        bottom_right_x=56.0,
        low_ctrl_left=(14.0, 69.0),
        low_ctrl_right=(86.0, 69.0),
        deep_ctrl_left=(33.0, 84.0),
        deep_ctrl_right=(67.0, 84.0),
    )


def shape_v_outline(x: float, y: float) -> bool:
    return shape_v(x, y, 14.5) and not shape_v(x, y, 10.5)


def shield_inner(x: float, y: float) -> bool:
    return shield_shape(
        x,
        y,
        top_y=14.0,
        side_top_y=26.0,
        side_bottom_y=56.0,
        mid_y=70.0,
        bottom_y=84.0,
        left_side_x=20.0,
        right_side_x=80.0,
        top_left_x=31.0,
        top_right_x=69.0,
        mid_left_x=29.0,
        mid_right_x=71.0,
        bottom_left_x=43.5,
        bottom_right_x=56.5,
        low_ctrl_left=(20.0, 63.0),
        low_ctrl_right=(80.0, 63.0),
        deep_ctrl_left=(36.0, 77.0),
        deep_ctrl_right=(64.0, 77.0),
    )


def shield_core(x: float, y: float) -> bool:
    return shield_shape(
        x,
        y,
        top_y=19.0,
        side_top_y=29.0,
        side_bottom_y=52.0,
        mid_y=64.0,
        bottom_y=78.0,
        left_side_x=25.0,
        right_side_x=75.0,
        top_left_x=33.0,
        top_right_x=67.0,
        mid_left_x=33.0,
        mid_right_x=67.0,
        bottom_left_x=43.2,
        bottom_right_x=56.8,
        low_ctrl_left=(25.0, 58.0),
        low_ctrl_right=(75.0, 58.0),
        deep_ctrl_left=(38.5, 70.0),
        deep_ctrl_right=(61.5, 70.0),
    )


def sample_icon(x: float, y: float) -> tuple[float, float, float, float]:
    in_outer = shield_outer(x, y)
    if not in_outer:
        return (0.0, 0.0, 0.0, 0.0)

    in_inner = shield_inner(x, y)
    in_core = shield_core(x, y)

    t = clamp((x * 0.62 + y * 0.38) / 100.0, 0.0, 1.0)
    bg = mix((13, 24, 41), (76, 29, 149), t)
    glow = clamp(1.0 - (((x - 30.0) / 100.0) ** 2 + ((y - 20.0) / 100.0) ** 2) * 2.3, 0.0, 1.0) * 0.18
    bg = mix(tuple(int(c) for c in bg), (0, 212, 255), glow)

    color = (bg[0], bg[1], bg[2], 1.0)

    if not in_inner:
        color = blend(color, (0.0, 212.0, 255.0), 0.95)
    elif not in_core:
        color = blend(color, (0.0, 153.0, 204.0), 0.9)

    # Soft V shadow for depth.
    shadow_v = shape_v(x - 1.5, y - 1.6)
    if shadow_v:
        color = blend(color, (8.0, 10.0, 18.0), 0.42)

    is_outline = shape_v_outline(x, y)
    if is_outline:
        color = blend(color, (5.0, 9.0, 20.0), 0.96)

    is_fill = shape_v(x, y)
    if is_fill:
        lt = clamp((y - 24.0) / 50.0, 0.0, 1.0)
        letter = mix((126, 249, 255), (0, 180, 255), lt)
        color = blend(color, letter, 1.0)

    return color


def render_icon(size: int, supersample: int = 3) -> bytes:
    pixels = bytearray(size * size * 4)
    inv = 1.0 / (supersample * supersample)

    for j in range(size):
        for i in range(size):
            r = g = b = a = 0.0
            for sy in range(supersample):
                for sx in range(supersample):
                    x = (i + (sx + 0.5) / supersample) * 100.0 / size
                    y = (j + (sy + 0.5) / supersample) * 100.0 / size
                    sr, sg, sb, sa = sample_icon(x, y)
                    r += sr
                    g += sg
                    b += sb
                    a += sa
            r *= inv
            g *= inv
            b *= inv
            a *= inv
            idx = (j * size + i) * 4
            pixels[idx] = int(clamp(r, 0.0, 255.0) + 0.5)
            pixels[idx + 1] = int(clamp(g, 0.0, 255.0) + 0.5)
            pixels[idx + 2] = int(clamp(b, 0.0, 255.0) + 0.5)
            pixels[idx + 3] = int(clamp(a * 255.0, 0.0, 255.0) + 0.5)

    return bytes(pixels)


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


def write_png(path: Path, size: int) -> bytes:
    rgba = render_icon(size)
    data = encode_png(size, size, rgba)
    path.write_bytes(data)
    return data


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


def write_svg(path: Path) -> None:
    svg = """<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 100 100\" role=\"img\" aria-label=\"Vibepit shield monogram\">\n  <defs>\n    <linearGradient id=\"bg\" x1=\"0%\" y1=\"0%\" x2=\"100%\" y2=\"100%\">\n      <stop offset=\"0%\" stop-color=\"#0d1829\"/>\n      <stop offset=\"100%\" stop-color=\"#4c1d95\"/>\n    </linearGradient>\n    <linearGradient id=\"fg\" x1=\"0%\" y1=\"0%\" x2=\"0%\" y2=\"100%\">\n      <stop offset=\"0%\" stop-color=\"#7ef9ff\"/>\n      <stop offset=\"100%\" stop-color=\"#00b4ff\"/>\n    </linearGradient>\n  </defs>\n  <path d=\"M14 22 Q14 8 28 8 H72 Q86 8 86 22 V60 Q86 69 76 76 Q67 84 56 90 H44 Q33 84 24 76 Q14 69 14 60 Z\" fill=\"#00d4ff\"/>\n  <path d=\"M20 26 Q20 14 31 14 H69 Q80 14 80 26 V56 Q80 63 71 70 Q64 77 56.5 84 H43.5 Q36 77 29 70 Q20 63 20 56 Z\" fill=\"#0099cc\"/>\n  <path d=\"M25 29 Q25 19 33 19 H67 Q75 19 75 29 V52 Q75 58 67 64 Q61.5 70 56.8 78 H43.2 Q38.5 70 33 64 Q25 58 25 52 Z\" fill=\"url(#bg)\"/>\n  <path d=\"M34 30 L50 72 L66 30\" fill=\"none\" stroke=\"#070d18\" stroke-width=\"14.5\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n  <path d=\"M34 30 L50 72 L66 30\" fill=\"none\" stroke=\"url(#fg)\" stroke-width=\"10.5\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n</svg>\n"""
    path.write_text(svg, encoding="utf-8")


def write_mask_svg(path: Path) -> None:
    svg = """<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 100 100\">\n  <path d=\"M14 22 Q14 8 28 8 H72 Q86 8 86 22 V60 Q86 69 76 76 Q67 84 56 90 H44 Q33 84 24 76 Q14 69 14 60 Z\" fill=\"black\"/>\n  <path d=\"M34 30 L50 72 L66 30\" fill=\"none\" stroke=\"white\" stroke-width=\"10.5\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n</svg>\n"""
    path.write_text(svg, encoding="utf-8")


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

    png16 = write_png(OUT / "favicon-16x16.png", 16)
    png32 = write_png(OUT / "favicon-32x32.png", 32)
    png48 = write_png(OUT / "favicon-48x48.png", 48)
    write_png(OUT / "apple-touch-icon.png", 180)
    write_png(OUT / "android-chrome-192x192.png", 192)
    write_png(OUT / "android-chrome-512x512.png", 512)

    write_ico(OUT / "favicon.ico", [(16, png16), (32, png32), (48, png48)])
    write_svg(OUT / "favicon.svg")
    write_mask_svg(OUT / "safari-pinned-tab.svg")
    write_manifest(OUT / "site.webmanifest")


if __name__ == "__main__":
    main()
