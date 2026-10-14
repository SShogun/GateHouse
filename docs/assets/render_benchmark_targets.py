"""Render the planning scorecard to SVG and PNG from its JSON source.

Requires Python + Pillow and Liberation fonts. Run from any directory:
  python render_benchmark_targets.py [--font-dir /usr/share/fonts/liberation]

This script renders targets; it does not run benchmarks or manufacture results.
"""

import argparse
import html
import json
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--font-dir", type=Path, default=Path("/usr/share/fonts/liberation"))
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    data = json.loads((root / "gatehouse-benchmark-targets.json").read_text())
    if data["status"] != "unmeasured_targets":
        raise ValueError("Use a separate result renderer for measured benchmark data")

    width, height, scale = 1720, 960, 2
    canvas = Image.new("RGB", (width * scale, height * scale), "white")
    draw = ImageDraw.Draw(canvas)
    svg = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-labelledby="title desc">',
           '<title id="title">GateHouse benchmark targets, not measured results</title>',
           '<desc id="desc">Six provisional goals; all GateHouse, Caddy, Traefik and Go proxy measurements remain pending. No performance ranking.</desc>']
    navy, muted, border = "#172642", "#62728c", "#dce3f0"
    fonts = {}

    def font(size, bold=False, mono=False):
        key = (size, bold, mono)
        if key not in fonts:
            face = "LiberationMono" if mono else "LiberationSans"
            face += "-Bold.ttf" if bold else "-Regular.ttf"
            fonts[key] = ImageFont.truetype(str(args.font_dir / face), size * scale)
        return fonts[key]

    def rect(x, y, w, h, fill, stroke=None):
        draw.rectangle((x * scale, y * scale, (x + w) * scale, (y + h) * scale), fill=fill, outline=stroke, width=scale)
        svg.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{fill}"' + (f' stroke="{stroke}"' if stroke else "") + '/>')

    def text(value, x, y, size=22, color=navy, bold=False, mono=False, center=False):
        face = font(size, bold, mono)
        if center:
            box = draw.textbbox((0, 0), value, font=face)
            px = x * scale - (box[2] - box[0]) / 2
        else:
            px = x * scale
        # SVG baseline aligns approximately with Pillow's top anchor.
        draw.text((px, y * scale), value, font=face, fill=color, anchor="lt")
        family = "Liberation Mono, monospace" if mono else "Liberation Sans, Arial, sans-serif"
        svg.append(f'<text x="{x}" y="{y + size * .82}" font-family="{family}" font-size="{size}" fill="{color}" font-weight="{700 if bold else 400}"' + (' text-anchor="middle"' if center else "") + f'>{html.escape(value)}</text>')

    rect(0, 0, width, height, "#ffffff")
    text(data["title"], 38, 36, 43, bold=True)
    text(data["subtitle"], 38, 99, 23, color=muted)
    rect(1395, 34, 287, 43, "#eef1ff")
    text("TARGETS · UNMEASURED", 1538, 47, 18, "#4354a2", bold=True, center=True)
    text("INITIAL PRODUCT GOALS", 38, 168, 16, color=muted, bold=True, mono=True)
    text("PROFILE-SCOPED · NO OVERALL SCORE", 1250, 168, 15, color=muted, mono=True)

    x0, y0 = 38, 212
    label_width, cell_width = 324, 220
    total = label_width + 6 * cell_width
    header_height, row_height = 84, 82
    rect(x0, y0, total, header_height, "#ffffff", border)
    for col, lines in enumerate(data["columns"]):
        x = x0 if col == 0 else x0 + label_width + (col - 1) * cell_width
        w = label_width if col == 0 else cell_width
        if col:
            rect(x, y0, w, header_height, "#ffffff", border)
        anchor = x + 22 if col == 0 else x + w / 2
        text(lines[0], anchor, y0 + 23, 17, color=muted, bold=True, center=col != 0)
        if lines[1]:
            text(lines[1], anchor, y0 + 51, 16, color=muted, center=col != 0)

    for idx, row in enumerate(data["rows"]):
        y = y0 + header_height + idx * row_height
        fill = "#eef1ff" if row["kind"] == "target" else ("#f8faff" if idx % 2 == 0 else "#ffffff")
        rect(x0, y, total, row_height, fill, border)
        text(row["name"], x0 + 22, y + 18, 25, bold=True)
        text(row["status"], x0 + 22, y + 54, 12, color=muted, mono=True)
        for col, value in enumerate(row["cells"]):
            x = x0 + label_width + col * cell_width
            if row["kind"] == "target":
                rect(x, y, cell_width, row_height, "#dce3ff" if col == 1 else "#e9edff", border)
            text(value, x + cell_width / 2, y + 28, 21, color=navy if row["kind"] == "target" else muted,
                 bold=row["kind"] == "target", mono=True, center=True)

    for idx, note in enumerate(data["notes"]):
        text(note, 38, 742 + idx * 31, 18, color=muted)
    text(data["date"] + " · Product hypothesis v1", 38, 917, 15, color=muted, mono=True)
    svg.append("</svg>")
    (root / "gatehouse-benchmark-targets.svg").write_text("\n".join(svg) + "\n")
    canvas.save(root / "gatehouse-benchmark-targets.png", optimize=True)


if __name__ == "__main__":
    main()
