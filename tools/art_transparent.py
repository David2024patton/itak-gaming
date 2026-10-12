#!/usr/bin/env python3
"""Download console photos from Wikimedia Commons and cut the studio
background out (border flood fill), saving transparent PNGs.

Usage: python3 art_transparent.py [outdir]
Prints one line per system: id<TAB>status<TAB>source filename used.
"""
import io
import json
import sys
import urllib.parse
import urllib.request
from collections import deque

from PIL import Image

UA = {"User-Agent": "iTaK-Gaming-Art/1.0 (family arcade project)"}

# Candidate Commons file names per system, best first.
CANDIDATES = {
    "nes": ["NES-Console-Set.png", "NES-Console-Set.jpg"],
    "snes": ["SNES-Mod1-Console-Set.png"],
    "genesis": ["Sega-Genesis-NA-Mk2-Console-Set.png"],
    "gameboy": ["Game-Boy-Original.png", "Game-Boy-Original.jpg"],
    "gba": ["Nintendo-Game-Boy-Advance-Purple-FL.png",
            "Nintendo-Game-Boy-Advance-Purple-FL.jpg"],
    "ps1": ["PSX-Console-wController.png", "PSX-Console-wController.jpg"],
    "n64": ["N64-Console-Set.png", "Nintendo-64-Console-Set.png"],
    "dc": ["Dreamcast-Console-Set.png", "Sega-Dreamcast-Console-Set.png"],
    "psp": ["PSP-1000-Console-Set.png", "Sony-PSP-1000-Body.png",
            "PSP-Console-Set.png"],
    "ds": ["Nintendo-DS-Lite-Console-Set.png", "Nintendo-DS-Lite-Black.png",
           "Nintendo-DS-Fat-Blue.png"],
    "saturn": ["Sega-Saturn-Console-Set-Mk1.png",
               "Sega-Saturn-Console-Set.png", "Sega-Saturn-Console.png"],
    "gc": ["GameCube-Console-Set.png", "Nintendo-GameCube-Console-Set.png",
           "GameCube-Set.png"],
}

SEARCH_TERMS = {
    "psp": "PSP console set",
    "ds": "Nintendo DS Lite console",
    "saturn": "Sega Saturn console set",
    "gc": "GameCube console set",
    "n64": "Nintendo 64 console set",
    "dc": "Dreamcast console set",
}


def fetch(url):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read(), r.headers.get_content_type()


def fetch_file(filename, width=900):
    q = urllib.parse.quote(filename)
    url = f"https://commons.wikimedia.org/wiki/Special:FilePath/{q}?width={width}"
    data, ctype = fetch(url)
    if "image" not in ctype:
        raise ValueError(f"not an image: {ctype}")
    return data


def search_file(term):
    q = urllib.parse.urlencode({
        "action": "query", "format": "json", "list": "search",
        "srsearch": f"filetype:bitmap {term}", "srnamespace": "6",
        "srlimit": "10",
    })
    data, _ = fetch(f"https://commons.wikimedia.org/w/api.php?{q}")
    hits = json.loads(data)["query"]["search"]
    for h in hits:
        title = h["title"].replace("File:", "")
        if title.lower().endswith(".png") and "console" in title.lower():
            return title
    for h in hits:
        title = h["title"].replace("File:", "")
        if "console" in title.lower():
            return title
    return None


def cut_background(img):
    """Flood fill from the borders; background pixels go transparent."""
    img = img.convert("RGBA")
    w, h = img.size
    px = img.load()
    corners = [px[2, 2], px[w - 3, 2], px[2, h - 3], px[w - 3, h - 3]]
    bg = tuple(sum(c[i] for c in corners) // 4 for i in range(3))

    def dist(a, b):
        return max(abs(a[0] - b[0]), abs(a[1] - b[1]), abs(a[2] - b[2]))

    def flood(tol):
        seen = bytearray(w * h)
        queue = deque()
        for x in range(w):
            queue.append((x, 0))
            queue.append((x, h - 1))
        for y in range(h):
            queue.append((0, y))
            queue.append((w - 1, y))
        mask = bytearray(w * h)
        while queue:
            x, y = queue.popleft()
            i = y * w + x
            if seen[i]:
                continue
            seen[i] = 1
            if dist(px[x, y], bg) <= tol:
                mask[i] = 1
                if x + 1 < w:
                    queue.append((x + 1, y))
                if x > 0:
                    queue.append((x - 1, y))
                if y + 1 < h:
                    queue.append((x, y + 1))
                if y > 0:
                    queue.append((x, y - 1))
        return mask

    # Pass 1: strictly background-colored pixels connected to the edge.
    # (No neighbor-drift rule: it creeps across smooth console shading
    # and eats dark consoles like the Genesis and Saturn.)
    mask = flood(46)
    removed = 0
    for y in range(h):
        for x in range(w):
            if mask[y * w + x]:
                p = px[x, y]
                px[x, y] = (p[0], p[1], p[2], 0)
                removed += 1
    frac = removed / (w * h)
    # Feather: semi-transparent ring where removed meets kept.
    alpha = img.split()[3]
    bbox = alpha.getbbox()
    if bbox:
        img = img.crop(bbox)
    return img, frac


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else "sysart_png"
    import os
    os.makedirs(outdir, exist_ok=True)
    for sid, names in CANDIDATES.items():
        used = None
        data = None
        for name in names:
            try:
                data = fetch_file(name)
                used = name
                break
            except Exception:
                continue
        if used is None and sid in SEARCH_TERMS:
            found = search_file(SEARCH_TERMS[sid])
            if found:
                try:
                    data = fetch_file(found)
                    used = found
                except Exception:
                    pass
        if used is None:
            print(f"{sid}\tFAILED\t-")
            continue
        try:
            img = Image.open(io.BytesIO(data))
            out, frac = cut_background(img)
            if frac > 0.93:
                print(f"{sid}\tSUSPICIOUS({frac:.0%} removed)\t{used}")
            out.save(f"{outdir}/{sid}.png")
            print(f"{sid}\tok removed={frac:.0%} size={out.size}\t{used}")
        except Exception as e:
            print(f"{sid}\tERROR {e}\t{used}")


if __name__ == "__main__":
    main()
