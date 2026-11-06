#!/usr/bin/env python3
"""Draws a real pattern from the mini-program as a crisp PNG.

Usage: pattern_png.py <data.json> <mard.ts> <out.png> [width]

data.json holds {"size": 58, "cells": ["M4", ...]}: the page's own output, read
with `wechatide automation_page_action --action getData`. Colours come from the
app's MARD table, so the picture shows exactly what the tool produced. Nothing is
retouched: a screenshot of the same page matches it apart from resolution.
"""
import json
import re
import struct
import sys
import zlib


def palette(path):
    return {code: hexa for code, hexa in re.findall(r"([A-Z]+\d+):\s*'#([0-9A-Fa-f]{6})'", open(path).read())}


def write_png(path, width, height, rows):
    def chunk(tag, data):
        body = tag + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body))

    raw = b"".join(b"\x00" + row for row in rows)
    with open(path, "wb") as f:
        f.write(b"\x89PNG\r\n\x1a\n")
        f.write(chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)))
        f.write(chunk(b"IDAT", zlib.compress(raw, 9)))
        f.write(chunk(b"IEND", b""))


def main():
    data_path, mard_path, out = sys.argv[1:4]
    target = int(sys.argv[4]) if len(sys.argv) > 4 else 920
    data = json.load(open(data_path))
    colours = palette(mard_path)
    size, cells = data["size"], data["cells"]
    if len(cells) != size * size:
        sys.exit(f"expected {size * size} cells, got {len(cells)}")
    missing = {c for c in cells if c and c not in colours}
    if missing:
        sys.exit(f"colours missing from the palette: {sorted(missing)}")
    cell = max(2, target // size)
    # No hairlines between beads: resizing the image for a cover turns them into moire.
    gap = 0
    width = size * cell
    board = bytes((244, 240, 232))
    rows = []
    for y in range(size):
        line = bytearray()
        for x in range(size):
            code = cells[y * size + x]
            pixel = bytes.fromhex(colours[code]) if code else board
            line += pixel * (cell - gap) + board * gap
        rows.extend([bytes(line)] * (cell - gap))
        rows.extend([board * width] * gap)
    write_png(out, width, width, rows)
    print(out, width)


if __name__ == "__main__":
    main()
