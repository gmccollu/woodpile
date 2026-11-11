#!/usr/bin/env python3
"""Makes the site's images from the UI's mock-mode screenshots (neutral demo data).

    python3 site/tools/images.py            # uses web/screenshots/dashboard-{light,dark}.png

Needs Pillow (pip install pillow). Writes into site/img/:
  dashboard-light.webp, dashboard-dark.webp   the hero, one per colour mode
  dashboard-light.png                         fallback for browsers without WebP (palette PNG)
  og.jpg                                      1200x630 Open Graph image
"""
import pathlib
import sys

from PIL import Image

root = pathlib.Path(__file__).resolve().parents[2]
src = root / 'web' / 'screenshots'
out = root / 'site' / 'img'
out.mkdir(parents=True, exist_ok=True)

WIDTH = 1200


def load(mode):
    p = src / f'dashboard-{mode}.png'
    if not p.exists():
        sys.exit(f'missing {p}: run the UI screenshots first (cd web && npm run dev:mock, then npm run screenshots -- dashboard)')
    im = Image.open(p).convert('RGB')
    return im.resize((WIDTH, round(im.height * WIDTH / im.width)), Image.LANCZOS)


for mode in ('light', 'dark'):
    im = load(mode)
    im.save(out / f'dashboard-{mode}.webp', 'WEBP', quality=76, method=6)
    print(f'dashboard-{mode}.webp', (out / f'dashboard-{mode}.webp').stat().st_size)

light = load('light')
fallback = light.resize((800, round(light.height * 800 / WIDTH)), Image.LANCZOS)
fallback.quantize(colors=64, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).save(out / 'dashboard-light.png', optimize=True)
print('dashboard-light.png', (out / 'dashboard-light.png').stat().st_size)

# Open Graph: 1200x630, the top of the light dashboard.
og = light.crop((0, 0, 1200, 630))
og.save(out / 'og.jpg', 'JPEG', quality=78, optimize=True, progressive=True)
print('og.jpg', (out / 'og.jpg').stat().st_size)
