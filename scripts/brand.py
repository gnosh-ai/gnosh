#!/usr/bin/env python3
"""Draws Klash's brand images into docs/brand/.

The wordmark is the TUI's own: the half-block letterforms in
internal/ui/logo/letterforms.go, where each terminal cell holds two square
pixels. Drawing the same strings here keeps the banner on GitHub and the
logo in the terminal the same shape. The colors are the TUI's too: Crush's
purple and hot pink, with green and gold at the ends of every fade.

    python3 scripts/brand.py

writes banner.svg, divider-dark.svg, divider-light.svg, signoff.svg,
avatar.svg, favicon.svg and og.html. The PNGs are screenshots of the last two in
headless Chromium:

    chromium --headless --hide-scrollbars --window-size=1280,640 \\
        --screenshot=docs/brand/og.png docs/brand/og.html
    chromium --headless --hide-scrollbars --window-size=512,512 \\
        --screenshot=docs/brand/avatar.png docs/brand/avatar.svg

The stars are drawn from a fixed seed, so the files only change when this
script does.
"""

import math
import os
import random
from string import Template

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "docs", "brand")

# ── Palette ──────────────────────────────────────────────────────────────
# Deep space in one flat color, Crush's purple and hot pink (Charmtone's
# Charple and Dolly) in the word, and gold and mint only as starlight.
VOID = "#07060D"
NIGHT = "#0D0A1A"
RIM = "#2D2C36"
HAZE = "#858392"
MOON = "#ECEBF0"
STAR = "#FFFAF1"
LAVENDER = "#C9C1FF"
PURPLE = "#6B50FF"
PINK = "#FF60FF"
GOLD = "#FED835"
AMBER = "#FEA408"
MINT = "#68FFD6"
GREEN = "#00FFB2"
# Every fade, as in the TUI: green, then Crush's purple and hot pink, then
# gold. Purple and pink hold the middle; green and gold only the ends.
NEBULA = (GREEN, PURPLE, PINK, GOLD)
# Deeper versions of the gradient that read on a white page.
INK_PURPLE = "#5B40EC"
INK_MAGENTA = "#B02DCB"

MONO = "SFMono-Regular,Menlo,Consolas,'Liberation Mono','DejaVu Sans Mono',monospace"

# ── Letterforms ──────────────────────────────────────────────────────────
# Three rows of half-block text per letter, as in letterforms.go. A
# stretchable letter repeats its middle column.
LETTERS = {
    "K": (["█ ▄▀", "█▀▄ ", "▀  ▀"], None),
    "L": (["█", "█", "▀"], ([" ", " ", "▀"], 4, None)),
    "A": (["▄", "█", "▀"], (["▀", "▀", " "], 3, ["▄", "█", "▀"])),
    "S": (["▄", "▀", "▀"], (["▀", "▀", "▀"], 3, ["▀", "█", " "])),
    "H": (["█", "█", "▀"], ([" ", "▀", " "], 3, ["█", "█", "▀"])),
}


def letter_rows(ch, stretch=None):
    left, mid = LETTERS[ch]
    if mid is None:
        return left
    middle, width, right = mid
    n = stretch or width
    return [left[r] + middle[r] * n + (right[r] if right else "") for r in range(3)]


def word_rows(word="KLASH", spacing=1):
    """The word as three rows of half-block text, one cell between letters."""
    parts = [letter_rows(c) for c in word]
    rows = ["", "", ""]
    for i, part in enumerate(parts):
        w = max(len(r) for r in part)
        for r in range(3):
            rows[r] += part[r].ljust(w)
            if i < len(parts) - 1:
                rows[r] += " " * spacing
    return rows


def pixels(rows):
    """Half-block rows to a set of (column, pixel row) squares."""
    on = set()
    for r, row in enumerate(rows):
        for c, ch in enumerate(row):
            if ch in "█▀":
                on.add((c, 2 * r))
            if ch in "█▄":
                on.add((c, 2 * r + 1))
    return on


def runs_path(on, x0, y0, p, h=None):
    """One rectangle per horizontal run of pixels: short, and seam-free."""
    h = p if h is None else h
    d = []
    rows = {}
    for c, r in on:
        rows.setdefault(r, []).append(c)
    for r in sorted(rows):
        cols = sorted(rows[r])
        start = prev = cols[0]
        for c in cols[1:] + [None]:
            if c is not None and c == prev + 1:
                prev = c
                continue
            w = (prev - start + 1) * p
            d.append(f"M{x0 + start * p:g} {y0 + r * p:g}h{w:g}v{h:g}h{-w:g}z")
            if c is not None:
                start = prev = c
    return "".join(d)


def top_edges(on):
    return {(c, r) for c, r in on if (c, r - 1) not in on}


# ── Pieces shared by the images ──────────────────────────────────────────

def fmt(v):
    return f"{v:.1f}".rstrip("0").rstrip(".")


def svg_open(w, h, label, extra=""):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" '
        f'width="{w}" height="{h}" role="img" aria-label="{label}"{extra}>'
        f"<title>{label}</title>"
    )


def make_stars(rng, n, w, ymax, keepout):
    """Mostly white and lavender; gold and mint only among the brightest."""
    stars = []
    tries = 0
    while len(stars) < n and tries < n * 50:
        tries += 1
        x, y = rng.uniform(8, w - 8), rng.uniform(8, ymax)
        kx0, ky0, kx1, ky1 = keepout
        if kx0 < x < kx1 and ky0 < y < ky1:
            continue
        roll = rng.random()
        if roll < 0.72:
            r, col, op = rng.uniform(0.55, 1.05), STAR, rng.uniform(0.35, 0.75)
        elif roll < 0.94:
            r, col, op = rng.uniform(1.0, 1.6), rng.choice([STAR, LAVENDER, PINK]), rng.uniform(0.6, 0.95)
        else:
            r, col, op = rng.uniform(1.4, 2.0), rng.choice([GOLD, MINT, STAR, LAVENDER]), 1.0
        stars.append((x, y, r, col, op))
    return stars


def sparkle(x, y, s, col, attrs=""):
    """A four-point star: two thin rays crossing over a bright dot."""
    k = s * 0.12
    return (
        f'<g{attrs} transform="translate({x:.1f} {y:.1f})">'
        f'<path d="M0 {-s:.1f}L{k:.2f} {-k:.2f}L{s:.1f} 0L{k:.2f} {k:.2f}'
        f'L0 {s:.1f}L{-k:.2f} {k:.2f}L{-s:.1f} 0L{-k:.2f} {-k:.2f}z" fill="{col}"/>'
        f'<circle r="{s * 0.22:.2f}" fill="{STAR}"/></g>'
    )


def sky(rng, w, h, keepout, n_stars, twinkle):
    """Deep space, edge to edge: one flat color and crisp stars. No clouds,
    haze or planet; the word floats in space."""
    o = [f'<rect width="{w}" height="{h}" fill="{NIGHT}"/>']
    for x, y, rad, col, op in make_stars(rng, n_stars, w, h - 8, keepout):
        if rad > 1.5 and rng.random() < 0.6:
            attrs = f' class="tw" style="animation-delay:-{rng.uniform(0, 4):.2f}s"' if twinkle else ""
            o.append(sparkle(x, y, rad * 3.2, col, attrs))
            continue
        attrs = ""
        if twinkle and rng.random() < 0.18:
            attrs = f' class="tw" style="animation-delay:-{rng.uniform(0, 4):.2f}s;animation-duration:{rng.uniform(3, 6):.1f}s"'
        o.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="{rad:.2f}" fill="{col}" opacity="{op:.2f}"{attrs}/>')
    return "".join(o)


def diagonals(x_from, x_to, y, rows, cw, ch, step=0):
    """Rows of the TUI's ╱ field between x_from and x_to.

    With step set, each row is that many cells shorter on its right end,
    the stair the TUI's right field makes.
    """
    d = []
    for i in range(rows):
        top = y + i * ch
        end = x_to - i * step * cw
        x = x_from
        while x + cw <= end + 0.01:
            d.append(f"M{fmt(x)} {fmt(top + ch)}L{fmt(x + cw)} {fmt(top)}")
            x += cw
    return "".join(d)


def word_defs(x0, y0, word_w, word_h, stops=NEBULA, gid="word"):
    """The word's fade, left to right with a little slope, through stops."""
    n = len(stops) - 1
    marks = "".join(f'<stop offset="{i / n:.3g}" stop-color="{c}"/>' for i, c in enumerate(stops))
    return (
        f'<linearGradient id="{gid}" gradientUnits="userSpaceOnUse" x1="{fmt(x0)}" y1="{fmt(y0)}" '
        f'x2="{fmt(x0 + word_w)}" y2="{fmt(y0 + word_h)}">{marks}</linearGradient>'
    )


def word_layers(on, x0, y0, p, bevel=True):
    """Face and a lit top edge for a pixel word, with crisp edges: no glow,
    no drop shadow and no light sweeping across it. The word floats in
    space, with nothing behind it."""
    o = [f'<path d="{runs_path(on, x0, y0, p)}" fill="url(#word)"/>']
    if bevel:
        o.append(f'<path d="{runs_path(top_edges(on), x0, y0, p, h=min(3, max(1.5, p / 8)))}" fill="#fff" opacity=".38"/>')
    return "".join(o)


def field_gradient(gid, x1, x2, peak=".55"):
    return (
        f'<linearGradient id="{gid}" gradientUnits="userSpaceOnUse" x1="{fmt(x1)}" y1="0" x2="{fmt(x2)}" y2="0">'
        f'<stop offset="0" stop-color="{PURPLE}" stop-opacity="0"/><stop offset=".2" stop-color="{PURPLE}" stop-opacity="{peak}"/>'
        f'<stop offset=".8" stop-color="{PINK}" stop-opacity="{peak}"/><stop offset="1" stop-color="{PINK}" stop-opacity="0"/></linearGradient>'
    )


def anim():
    """Twinkling stars, which hold still for reduced motion."""
    css = [
        "@keyframes tw{0%,100%{opacity:.15}50%{opacity:1}}",
        ".tw{animation:tw 4s ease-in-out infinite}",
        "@media (prefers-reduced-motion:reduce){.tw{animation:none}}",
    ]
    return "<style>\n" + "\n".join(css) + "\n</style>"


# ── Images ───────────────────────────────────────────────────────────────

def banner(W=1280, H=520, p=24):
    """The README's banner: the wordmark floating in space."""
    rng = random.Random(7)
    cw, ch = p, 2 * p
    rows = word_rows()
    on = pixels(rows)
    word_w, word_h = len(rows[0]) * p, 5 * p
    x0, y0 = round((W - word_w) / 2), 130
    meta_y = y0 + 3 * ch  # the TUI puts its meta row one cell under the word

    layers = word_layers(on, x0, y0, p)
    keep = (x0 - 40, y0 - 24, x0 + word_w + 40, meta_y + ch + 16)
    lx0 = x0 - 2 * cw - cw * math.ceil(x0 / cw)
    left = diagonals(lx0, x0 - cw, y0, 4, cw, ch)
    right = diagonals(x0 + word_w + cw, W + cw, y0, 4, cw, ch)

    o = [svg_open(W, H, "Klash, a security-first harness for coding agents")]
    o.append(anim())
    o.append("<defs>")
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(field_gradient("field", 0, W))
    o.append(f'<clipPath id="frame"><rect width="{W}" height="{H}" rx="22"/></clipPath>')
    o.append("</defs>")
    o.append('<g clip-path="url(#frame)">')
    o.append(sky(rng, W, H, keep, 165, twinkle=True))
    o.append(f'<path d="{left}{right}" stroke="url(#field)" stroke-width="2" fill="none"/>')
    ty = meta_y + ch * 0.66
    o.append(f'<text x="{x0}" y="{fmt(ty)}" font-family="{MONO}" font-size="21" fill="{GOLD}">klash.pro</text>')
    o.append(f'<text x="{x0 + word_w}" y="{fmt(ty)}" text-anchor="end" font-family="{MONO}" font-size="21" fill="{HAZE}">security-first agent harness</text>')
    o.append(layers)
    o.append("</g>")
    o.append(f'<rect x=".5" y=".5" width="{W - 1}" height="{H - 1}" rx="22" fill="none" stroke="{RIM}"/>')
    o.append("</svg>")
    return "\n".join(o)


def divider(light=False, W=1280, H=24):
    """A row of the TUI's ╱ field, faded at both ends."""
    a, b = (INK_PURPLE, INK_MAGENTA) if light else (PURPLE, PINK)
    peak = ".75" if light else ".65"
    o = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" aria-hidden="true">']
    o.append(f'<defs><linearGradient id="f" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{W}" y2="0"><stop offset="0" stop-color="{a}" stop-opacity="0"/><stop offset=".3" stop-color="{a}" stop-opacity="{peak}"/><stop offset=".7" stop-color="{b}" stop-opacity="{peak}"/><stop offset="1" stop-color="{b}" stop-opacity="0"/></linearGradient></defs>')
    o.append(f'<path d="{diagonals(0, W, 0, 1, 12, H)}" stroke="url(#f)" stroke-width="1.6" fill="none"/>')
    o.append("</svg>")
    return "".join(o)


def signoff(W=1280, H=260, p=10):
    """The sidebar's compact logo, as a closing mark: fields above and below,
    without stars, as the TUI draws it."""
    rng = random.Random(11)
    cw, ch = p, 2 * p
    rows = word_rows()
    on = pixels(rows)
    word_w, word_h = len(rows[0]) * p, 5 * p
    x0 = round((W - word_w) / 2)
    # Two field rows, the word (three cells), the meta row, one field row.
    top = round((H - 7 * ch) / 2) - 18
    y0 = top + 2 * ch
    meta_y = y0 + 3 * ch
    keep = (x0 - 30, top - 10, x0 + word_w + 30, meta_y + 2 * ch + 10)
    fields = diagonals(x0, x0 + word_w, top, 2, cw, ch) + diagonals(x0, x0 + word_w, meta_y + ch, 1, cw, ch)

    o = [svg_open(W, H, "Klash")]
    o.append(anim())
    o.append("<defs>")
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(f'<linearGradient id="field" gradientUnits="userSpaceOnUse" x1="{x0}" y1="0" x2="{x0 + word_w}" y2="0"><stop offset="0" stop-color="{PINK}" stop-opacity=".75"/><stop offset="1" stop-color="{PURPLE}" stop-opacity=".75"/></linearGradient>')
    o.append(f'<clipPath id="frame"><rect width="{W}" height="{H}" rx="22"/></clipPath>')
    o.append("</defs>")
    o.append('<g clip-path="url(#frame)">')
    o.append(sky(rng, W, H, keep, 90, twinkle=True))
    o.append(f'<path d="{fields}" stroke="url(#field)" stroke-width="1.4" fill="none"/>')
    o.append(f'<text x="{x0}" y="{fmt(meta_y + ch * 0.72)}" font-family="{MONO}" font-size="13" fill="{GOLD}">klash.pro</text>')
    o.append(word_layers(on, x0, y0, p))
    o.append("</g>")
    o.append(f'<rect x=".5" y=".5" width="{W - 1}" height="{H - 1}" rx="22" fill="none" stroke="{RIM}"/>')
    o.append("</svg>")
    return "\n".join(o)


def avatar(S=512, p=60):
    """The organisation's picture: the wordmark's K in space."""
    rng = random.Random(3)
    rows = letter_rows("K")
    on = pixels(rows)
    word_w, word_h = 4 * p, 5 * p
    x0 = round((S - word_w) / 2)
    y0 = round((S - word_h) / 2) - 22
    keep = (x0 - 30, y0 - 30, x0 + word_w + 30, y0 + word_h + 30)

    o = [svg_open(S, S, "Klash")]
    o.append("<defs>")
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append("</defs>")
    o.append(sky(rng, S, S, keep, 75, twinkle=False))
    o.append(word_layers(on, x0, y0, p))
    o.append(sparkle(x0 + word_w + 34, y0 + 18, 15, GOLD))
    o.append("</svg>")
    return "\n".join(o)


def favicon(S=64, p=10):
    """The tab icon: the K on a dark rounded square, readable on light tabs too."""
    on = pixels(letter_rows("K"))
    x0, y0 = (S - 4 * p) / 2, (S - 5 * p) / 2
    o = [svg_open(S, S, "Klash")]
    o.append("<defs>" + word_defs(x0, y0, 4 * p, 5 * p) + "</defs>")
    o.append(f'<rect width="{S}" height="{S}" rx="{S * 0.22:g}" fill="{NIGHT}"/>')
    o.append(f'<path d="{runs_path(on, x0, y0, p)}" fill="url(#word)"/>')
    o.append("</svg>")
    return "".join(o)


OG_HTML = Template("""<!doctype html>
<html><head><meta charset="utf-8">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@500;600&family=JetBrains+Mono:wght@400;500;700&display=block" rel="stylesheet">
<style>
*{box-sizing:border-box;margin:0;padding:0}
html,body{width:1280px;height:640px;overflow:hidden;background:$void}
.bg{position:absolute;inset:0}
.wrap{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center}
.tag{margin-top:356px;font:600 37px/1.2 Inter,system-ui,sans-serif;letter-spacing:-.012em;color:$moon}
.tag b{font-weight:600;background:linear-gradient(90deg,$green,$purple 35%,$pink 70%,$gold);-webkit-background-clip:text;background-clip:text;color:transparent}
.pts{margin-top:26px;display:flex;gap:14px;font:500 19px/1 'JetBrains Mono',monospace;color:#D6D3DC}
.pts span{display:flex;align-items:center;gap:10px;padding:11px 16px;border:1px solid $rim;border-radius:999px;background:rgba(13,10,26,.72)}
.pts i{width:9px;height:9px;border-radius:2px;display:block}
</style></head><body>
<div class="bg">$svg</div>
<div class="wrap">
  <div class="tag">A <b>security-first</b> harness for coding agents, in your terminal.</div>
  <div class="pts">
    <span><i style="background:$purple"></i>nothing phones home</span>
    <span><i style="background:$pink"></i>any model, hosted or local</span>
    <span><i style="background:$gold"></i>speaks Claude Code</span>
  </div>
</div>
</body></html>
""")


def og_svg(W=1280, H=640, p=28):
    """The social card's picture; og.html sets the words over it."""
    rng = random.Random(5)
    cw, ch = p, 2 * p
    rows = word_rows()
    on = pixels(rows)
    word_w, word_h = len(rows[0]) * p, 5 * p
    x0, y0 = round((W - word_w) / 2), 92
    meta_y = y0 + 3 * ch
    keep = (x0 - 40, y0 - 30, x0 + word_w + 40, 500)
    lx0 = x0 - cw - cw * math.ceil((x0 - cw) / cw)
    left = diagonals(lx0, x0 - cw, y0, 4, cw, ch)
    right = diagonals(x0 + word_w + cw, W + cw, y0, 4, cw, ch)

    o = [svg_open(W, H, "Klash")]
    o.append("<defs>")
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(field_gradient("field", 0, W))
    o.append("</defs>")
    o.append(sky(rng, W, H, keep, 145, twinkle=False))
    o.append(f'<path d="{left}{right}" stroke="url(#field)" stroke-width="2.2" fill="none"/>')
    ty = meta_y + ch * 0.66
    o.append(f'<text x="{x0}" y="{fmt(ty)}" font-family="JetBrains Mono,{MONO}" font-weight="500" font-size="24" fill="{GOLD}">klash.pro</text>')
    o.append(f'<text x="{x0 + word_w}" y="{fmt(ty)}" text-anchor="end" font-family="JetBrains Mono,{MONO}" font-size="24" fill="{HAZE}">github.com/klash-pro/klash</text>')
    o.append(word_layers(on, x0, y0, p))
    o.append("</svg>")
    return "\n".join(o)


def og_html():
    return OG_HTML.substitute(svg=og_svg(), void=VOID, green=GREEN, pink=PINK, purple=PURPLE, gold=GOLD, moon=MOON, rim=RIM)


def main():
    os.makedirs(OUT, exist_ok=True)
    files = {
        "banner.svg": banner(),
        "divider-dark.svg": divider(),
        "divider-light.svg": divider(light=True),
        "signoff.svg": signoff(),
        "avatar.svg": avatar(),
        "favicon.svg": favicon(),
        "og.html": og_html(),
    }
    for name, body in files.items():
        with open(os.path.join(OUT, name), "w") as f:
            f.write(body if body.endswith("\n") else body + "\n")
        print(f"wrote docs/brand/{name}")


if __name__ == "__main__":
    main()
