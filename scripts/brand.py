#!/usr/bin/env python3
"""Draws Klash's brand images into docs/brand/.

The wordmark is the TUI's own: the half-block letterforms in
internal/ui/logo/letterforms.go, where each terminal cell holds two square
pixels. Drawing the same strings here keeps the banner on GitHub and the
logo in the terminal the same shape.

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
# Deep space; an aurora green that fades through lime to starlight gold.
VOID = "#050A0B"
NIGHT = "#08110E"
HORIZON = "#0A1A14"
GROUND = "#040907"
RIM = "#22352C"
HAZE = "#819183"
MOON = "#DEDFD3"
STAR = "#F8F6E7"
AURORA = "#12E89A"
MINT = "#5BF0B8"
LIME = "#BBF132"
GOLD = "#FED835"
AMBER = "#FEA408"
MOSS = "#0B3B2A"
TEAL = "#14B8A6"
# Deeper versions of the gradient that read on a white page.
PINE = "#0A8F5C"
OCHRE = "#B98500"

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


def sky_defs(w, h, horizon):
    """Gradients for the sky, aurora, sunrise and the planet's rim."""
    return f"""
<linearGradient id="sky" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="{VOID}"/><stop offset=".62" stop-color="{NIGHT}"/><stop offset="1" stop-color="{HORIZON}"/></linearGradient>
<radialGradient id="aur1"><stop offset="0" stop-color="{AURORA}" stop-opacity=".30"/><stop offset="1" stop-color="{AURORA}" stop-opacity="0"/></radialGradient>
<radialGradient id="aur2"><stop offset="0" stop-color="{LIME}" stop-opacity=".16"/><stop offset="1" stop-color="{LIME}" stop-opacity="0"/></radialGradient>
<radialGradient id="aur3"><stop offset="0" stop-color="{TEAL}" stop-opacity=".20"/><stop offset="1" stop-color="{TEAL}" stop-opacity="0"/></radialGradient>
<radialGradient id="sun"><stop offset="0" stop-color="{GOLD}" stop-opacity=".55"/><stop offset=".45" stop-color="{AMBER}" stop-opacity=".18"/><stop offset="1" stop-color="{AMBER}" stop-opacity="0"/></radialGradient>
<linearGradient id="ray" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="{AURORA}" stop-opacity="0"/><stop offset=".55" stop-color="{AURORA}" stop-opacity=".9"/><stop offset="1" stop-color="{AURORA}" stop-opacity="0"/></linearGradient>
<linearGradient id="rim" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{w}" y2="0"><stop offset="0" stop-color="{AURORA}" stop-opacity="0"/><stop offset=".3" stop-color="{AURORA}" stop-opacity=".7"/><stop offset=".5" stop-color="{GOLD}"/><stop offset=".7" stop-color="{AURORA}" stop-opacity=".7"/><stop offset="1" stop-color="{AURORA}" stop-opacity="0"/></linearGradient>
<filter id="soft" x="-10%" y="-300%" width="120%" height="700%"><feGaussianBlur stdDeviation="6"/></filter>
<filter id="hblur" x="-20%" y="-5%" width="140%" height="110%"><feGaussianBlur stdDeviation="5 0"/></filter>"""


def make_stars(rng, n, w, ymax, keepout):
    stars = []
    tries = 0
    while len(stars) < n and tries < n * 50:
        tries += 1
        x, y = rng.uniform(8, w - 8), rng.uniform(8, ymax)
        kx0, ky0, kx1, ky1 = keepout
        if kx0 < x < kx1 and ky0 < y < ky1:
            continue
        roll = rng.random()
        if roll < 0.70:
            r, col, op = rng.uniform(0.55, 1.05), STAR, rng.uniform(0.35, 0.75)
        elif roll < 0.93:
            r, col, op = rng.uniform(1.0, 1.6), rng.choice([STAR, GOLD, MINT]), rng.uniform(0.6, 0.95)
        else:
            r, col, op = rng.uniform(1.4, 2.0), rng.choice([GOLD, MINT, STAR]), 1.0
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


def sky(rng, w, h, horizon, keepout, n_stars, twinkle, rays=True, sun_rx=380):
    """Sky, aurora, stars, and a planet whose rim catches a rising sun."""
    o = [f'<rect width="{w}" height="{h}" fill="url(#sky)"/>']
    o.append('<g class="aur">')
    o.append(f'<ellipse cx="{w * .30:.0f}" cy="{h * .29:.0f}" rx="{w * .41:.0f}" ry="{h * .29:.0f}" fill="url(#aur1)" transform="rotate(-9 {w * .30:.0f} {h * .29:.0f})"/>')
    o.append(f'<ellipse cx="{w * .70:.0f}" cy="{h * .23:.0f}" rx="{w * .36:.0f}" ry="{h * .23:.0f}" fill="url(#aur2)" transform="rotate(7 {w * .70:.0f} {h * .23:.0f})"/>')
    o.append(f'<ellipse cx="{w * .52:.0f}" cy="{h * .12:.0f}" rx="{w * .47:.0f}" ry="{h * .21:.0f}" fill="url(#aur3)"/>')
    o.append("</g>")
    if rays:
        # Aurora curtains: thin vertical streaks along a gentle wave.
        r, n = [], 70
        for i in range(n):
            x = w * 0.03 + i * (w * 0.94) / n + rng.uniform(-6, 6)
            base = horizon * 0.48 + horizon * 0.09 * math.sin(i / n * 2 * math.pi * 1.3 + 0.6)
            hgt = rng.uniform(horizon * 0.21, horizon * 0.44)
            op = rng.uniform(0.05, 0.16) * (1 - abs(i - n / 2) / (n * 0.86))
            r.append(f'<rect x="{x:.1f}" y="{base - hgt:.1f}" width="{rng.uniform(4, 11):.1f}" height="{hgt:.1f}" fill="url(#ray)" opacity="{op:.3f}"/>')
        o.append(f'<g filter="url(#hblur)">{"".join(r)}</g>')
    for x, y, rad, col, op in make_stars(rng, n_stars, w, horizon - 30, keepout):
        if rad > 1.5 and rng.random() < 0.6:
            attrs = f' class="tw" style="animation-delay:-{rng.uniform(0, 4):.2f}s"' if twinkle else ""
            o.append(sparkle(x, y, rad * 3.2, col, attrs))
            continue
        attrs = ""
        if twinkle and rng.random() < 0.18:
            attrs = f' class="tw" style="animation-delay:-{rng.uniform(0, 4):.2f}s;animation-duration:{rng.uniform(3, 6):.1f}s"'
        o.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="{rad:.2f}" fill="{col}" opacity="{op:.2f}"{attrs}/>')
    pr = w * 2.03
    pcx, pcy = w / 2, horizon + pr
    o.append(f'<ellipse cx="{w / 2:.0f}" cy="{horizon}" rx="{sun_rx}" ry="{sun_rx * 0.16:.0f}" fill="url(#sun)"/>')
    o.append(f'<circle cx="{fmt(pcx)}" cy="{fmt(pcy)}" r="{fmt(pr)}" fill="{GROUND}"/>')
    o.append(f'<circle cx="{fmt(pcx)}" cy="{fmt(pcy)}" r="{fmt(pr)}" fill="none" stroke="url(#rim)" stroke-width="10" filter="url(#soft)" opacity=".8"/>')
    o.append(f'<circle cx="{fmt(pcx)}" cy="{fmt(pcy)}" r="{fmt(pr)}" fill="none" stroke="url(#rim)" stroke-width="1.6"/>')
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


def word_defs(x0, y0, word_w, word_h, a=AURORA, b=LIME, c=GOLD, gid="word"):
    return (
        f'<linearGradient id="{gid}" gradientUnits="userSpaceOnUse" x1="{fmt(x0)}" y1="{fmt(y0)}" '
        f'x2="{fmt(x0 + word_w)}" y2="{fmt(y0 + word_h)}"><stop offset="0" stop-color="{a}"/>'
        f'<stop offset=".5" stop-color="{b}"/><stop offset="1" stop-color="{c}"/></linearGradient>'
    )


def word_layers(on, x0, y0, p, glow=True, depth=True, bevel=True):
    """Glow, depth, face and a lit top edge for a pixel word."""
    face = runs_path(on, x0, y0, p)
    o = []
    if glow:
        o.append(f'<path d="{face}" fill="url(#word)" filter="url(#glow)" opacity=".55"/>')
    if depth:
        off = max(2, round(p * 0.21))
        o.append(f'<path d="{runs_path(on, x0 + off, y0 + off, p)}" fill="{MOSS}"/>')
    o.append(f'<path d="{face}" fill="url(#word)"/>')
    if bevel:
        o.append(f'<path d="{runs_path(top_edges(on), x0, y0, p, h=min(3, max(1.5, p / 8)))}" fill="#fff" opacity=".38"/>')
    return face, "".join(o)


def anim(sweep=None):
    """Twinkling stars and a breathing aurora; with sweep=(from, to), also a
    band of light crossing the word. All of it stops for reduced motion."""
    css = [
        "@keyframes tw{0%,100%{opacity:.15}50%{opacity:1}}",
        "@keyframes breathe{0%,100%{opacity:.75}50%{opacity:1}}",
        ".tw{animation:tw 4s ease-in-out infinite}",
        ".aur{animation:breathe 9s ease-in-out infinite}",
    ]
    if sweep:
        a, b = sweep
        css.append(f"@keyframes sweep{{0%{{transform:translateX({a}px)}}50%,100%{{transform:translateX({b}px)}}}}")
        css.append(".shine{animation:sweep 8s cubic-bezier(.45,0,.2,1) infinite}")
    css.append("@media (prefers-reduced-motion:reduce){.tw,.aur,.shine{animation:none}.shine{opacity:0}}")
    return "<style>\n" + "\n".join(css) + "\n</style>"


# ── Images ───────────────────────────────────────────────────────────────

def banner(W=1280, H=520, p=24):
    """The README's banner: the wordmark over a planet's sunrise."""
    rng = random.Random(7)
    cw, ch = p, 2 * p
    rows = word_rows()
    on = pixels(rows)
    word_w, word_h = len(rows[0]) * p, 5 * p
    x0, y0 = round((W - word_w) / 2), 178
    meta_y = y0 - ch  # the TUI puts its meta row one cell above the word
    horizon = 430

    face, layers = word_layers(on, x0, y0, p)
    keep = (x0 - 40, meta_y - 24, x0 + word_w + 40, y0 + 6 * p + 16)
    left = diagonals(x0 - 2 * cw - cw * math.ceil(x0 / cw), x0 - cw, meta_y, 4, cw, ch)
    right = diagonals(x0 + word_w + cw, W + cw, meta_y, 4, cw, ch)

    o = [svg_open(W, H, "Klash, a security-first harness for coding agents")]
    o.append(anim(sweep=(x0 - 360, x0 + word_w + 120)))
    o.append("<defs>")
    o.append(sky_defs(W, H, horizon))
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(f'<linearGradient id="field" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{W}" y2="0"><stop offset="0" stop-color="{AURORA}" stop-opacity="0"/><stop offset=".2" stop-color="{AURORA}" stop-opacity=".5"/><stop offset=".8" stop-color="{GOLD}" stop-opacity=".5"/><stop offset="1" stop-color="{GOLD}" stop-opacity="0"/></linearGradient>')
    o.append('<linearGradient id="shineg" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="#fff" stop-opacity="0"/><stop offset=".5" stop-color="#fff" stop-opacity=".8"/><stop offset="1" stop-color="#fff" stop-opacity="0"/></linearGradient>')
    o.append('<filter id="glow" x="-20%" y="-60%" width="140%" height="220%"><feGaussianBlur stdDeviation="14"/></filter>')
    o.append(f'<clipPath id="frame"><rect width="{W}" height="{H}" rx="22"/></clipPath>')
    o.append(f'<clipPath id="wordclip"><path d="{face}"/></clipPath>')
    o.append("</defs>")
    o.append('<g clip-path="url(#frame)">')
    o.append(sky(rng, W, H, horizon, keep, 130, twinkle=True))
    o.append(f'<path d="{left}{right}" stroke="url(#field)" stroke-width="2" fill="none"/>')
    ty = meta_y + ch * 0.66
    o.append(f'<text x="{x0}" y="{fmt(ty)}" font-family="{MONO}" font-size="21" fill="{GOLD}">klash.pro</text>')
    o.append(f'<text x="{x0 + word_w}" y="{fmt(ty)}" text-anchor="end" font-family="{MONO}" font-size="21" fill="{HAZE}">security-first agent harness</text>')
    o.append(layers)
    # A slanted band of light that crosses the word now and then.
    o.append(f'<g clip-path="url(#wordclip)"><g class="shine" transform="translate({x0 - 360} 0)"><rect y="{y0 - 40}" width="170" height="{word_h + 80}" fill="url(#shineg)" opacity=".85" transform="skewX(-24)"/></g></g>')
    o.append("</g>")
    o.append(f'<rect x=".5" y=".5" width="{W - 1}" height="{H - 1}" rx="22" fill="none" stroke="{RIM}"/>')
    o.append("</svg>")
    return "\n".join(o)


def divider(light=False, W=1280, H=24):
    """A row of the TUI's ╱ field, faded at both ends."""
    a, b = (PINE, OCHRE) if light else (AURORA, GOLD)
    peak = ".75" if light else ".6"
    o = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" aria-hidden="true">']
    o.append(f'<defs><linearGradient id="f" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{W}" y2="0"><stop offset="0" stop-color="{a}" stop-opacity="0"/><stop offset=".3" stop-color="{a}" stop-opacity="{peak}"/><stop offset=".7" stop-color="{b}" stop-opacity="{peak}"/><stop offset="1" stop-color="{b}" stop-opacity="0"/></linearGradient></defs>')
    o.append(f'<path d="{diagonals(0, W, 0, 1, 12, H)}" stroke="url(#f)" stroke-width="1.6" fill="none"/>')
    o.append("</svg>")
    return "".join(o)


def signoff(W=1280, H=260, p=10):
    """The sidebar's compact logo, as a closing mark: fields above and below."""
    rng = random.Random(11)
    cw, ch = p, 2 * p
    rows = word_rows()
    on = pixels(rows)
    word_w, word_h = len(rows[0]) * p, 5 * p
    x0 = round((W - word_w) / 2)
    # Two field rows, the meta row, the word (three cells), one field row.
    top = round((H - 7 * ch) / 2) - 18
    meta_y = top + 2 * ch
    y0 = meta_y + ch
    horizon = H - 34
    keep = (x0 - 30, top - 10, x0 + word_w + 30, y0 + 3 * ch + ch + 10)
    fields = diagonals(x0, x0 + word_w, top, 2, cw, ch) + diagonals(x0, x0 + word_w, y0 + 3 * ch, 1, cw, ch)

    o = [svg_open(W, H, "Klash")]
    o.append(anim())
    o.append("<defs>")
    o.append(sky_defs(W, H, horizon))
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(f'<linearGradient id="field" gradientUnits="userSpaceOnUse" x1="{x0}" y1="0" x2="{x0 + word_w}" y2="0"><stop offset="0" stop-color="{AURORA}" stop-opacity=".7"/><stop offset="1" stop-color="{GOLD}" stop-opacity=".7"/></linearGradient>')
    o.append('<filter id="glow" x="-20%" y="-60%" width="140%" height="220%"><feGaussianBlur stdDeviation="8"/></filter>')
    o.append(f'<clipPath id="frame"><rect width="{W}" height="{H}" rx="22"/></clipPath>')
    o.append("</defs>")
    o.append('<g clip-path="url(#frame)">')
    o.append(sky(rng, W, H, horizon, keep, 70, twinkle=True, rays=False, sun_rx=260))
    o.append(f'<path d="{fields}" stroke="url(#field)" stroke-width="1.4" fill="none"/>')
    o.append(f'<text x="{x0}" y="{fmt(meta_y + ch * 0.72)}" font-family="{MONO}" font-size="13" fill="{GOLD}">klash.pro</text>')
    o.append(word_layers(on, x0, y0, p)[1])
    o.append("</g>")
    o.append(f'<rect x=".5" y=".5" width="{W - 1}" height="{H - 1}" rx="22" fill="none" stroke="{RIM}"/>')
    o.append("</svg>")
    return "\n".join(o)


def avatar(S=512, p=60):
    """The organisation's picture: the wordmark's K over the sunrise."""
    rng = random.Random(3)
    rows = letter_rows("K")
    on = pixels(rows)
    word_w, word_h = 4 * p, 5 * p
    x0 = round((S - word_w) / 2)
    y0 = round((S - word_h) / 2) - 22
    horizon = 430
    keep = (x0 - 30, y0 - 30, x0 + word_w + 30, y0 + word_h + 30)

    o = [svg_open(S, S, "Klash")]
    o.append("<defs>")
    o.append(sky_defs(S, S, horizon))
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append('<filter id="glow" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="22"/></filter>')
    o.append("</defs>")
    o.append(sky(rng, S, S, horizon, keep, 60, twinkle=False, sun_rx=220))
    o.append(word_layers(on, x0, y0, p)[1])
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
html,body{width:1280px;height:640px;overflow:hidden;background:#050A0B}
.bg{position:absolute;inset:0}
.wrap{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center}
.tag{margin-top:356px;font:600 37px/1.2 Inter,system-ui,sans-serif;letter-spacing:-.012em;color:$moon}
.tag b{font-weight:600;background:linear-gradient(90deg,$aurora,$lime 55%,$gold);-webkit-background-clip:text;background-clip:text;color:transparent}
.pts{margin-top:26px;display:flex;gap:14px;font:500 19px/1 'JetBrains Mono',monospace;color:#C9CEC2}
.pts span{display:flex;align-items:center;gap:10px;padding:11px 16px;border:1px solid $rim;border-radius:999px;background:rgba(8,17,14,.72)}
.pts i{width:9px;height:9px;border-radius:2px;display:block}
</style></head><body>
<div class="bg">$svg</div>
<div class="wrap">
  <div class="tag">A <b>security-first</b> harness for coding agents, in your terminal.</div>
  <div class="pts">
    <span><i style="background:$aurora"></i>nothing phones home</span>
    <span><i style="background:$lime"></i>any model, hosted or local</span>
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
    x0, y0 = round((W - word_w) / 2), 148
    meta_y = y0 - ch
    horizon = 560
    keep = (x0 - 40, meta_y - 30, x0 + word_w + 40, 500)
    left = diagonals(x0 - cw - cw * math.ceil((x0 - cw) / cw), x0 - cw, meta_y, 4, cw, ch)
    right = diagonals(x0 + word_w + cw, W + cw, meta_y, 4, cw, ch)

    o = [svg_open(W, H, "Klash")]
    o.append("<defs>")
    o.append(sky_defs(W, H, horizon))
    o.append(word_defs(x0, y0, word_w, word_h))
    o.append(f'<linearGradient id="field" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="{W}" y2="0"><stop offset="0" stop-color="{AURORA}" stop-opacity="0"/><stop offset=".2" stop-color="{AURORA}" stop-opacity=".5"/><stop offset=".8" stop-color="{GOLD}" stop-opacity=".5"/><stop offset="1" stop-color="{GOLD}" stop-opacity="0"/></linearGradient>')
    o.append('<filter id="glow" x="-20%" y="-60%" width="140%" height="220%"><feGaussianBlur stdDeviation="16"/></filter>')
    o.append("</defs>")
    o.append(sky(rng, W, H, horizon, keep, 120, twinkle=False, sun_rx=420))
    o.append(f'<path d="{left}{right}" stroke="url(#field)" stroke-width="2.2" fill="none"/>')
    ty = meta_y + ch * 0.66
    o.append(f'<text x="{x0}" y="{fmt(ty)}" font-family="JetBrains Mono,{MONO}" font-weight="500" font-size="24" fill="{GOLD}">klash.pro</text>')
    o.append(f'<text x="{x0 + word_w}" y="{fmt(ty)}" text-anchor="end" font-family="JetBrains Mono,{MONO}" font-size="24" fill="{HAZE}">github.com/klash-pro/klash</text>')
    o.append(word_layers(on, x0, y0, p)[1])
    o.append("</svg>")
    return "\n".join(o)


def og_html():
    return OG_HTML.substitute(svg=og_svg(), aurora=AURORA, lime=LIME, gold=GOLD, moon=MOON, rim=RIM)


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
