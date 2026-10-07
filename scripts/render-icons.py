# Usage, from the repository root: python3 scripts/render-icons.py (needs numpy and Pillow).
# The PNG icons of the installable panel are rendered from the mark in web/favicon.svg; run it
# again only if the mark changes.
# Renders the OMNIBOX mark (web/favicon.svg: two rings, Ice → Iris → Ember gradient) on Deep Night.
import numpy as np
from PIL import Image

STOPS = [(0.0, (0xBE, 0xEF, 0xFF)), (0.48, (0x7C, 0x6C, 0xFF)), (0.82, (0xFF, 0x96, 0x54)), (1.0, (0xFF, 0xBE, 0x78))]
BG = (0x0A, 0x0C, 0x1F)

def render(size, scale):
    ss = 4
    n = size * ss
    y, x = np.mgrid[0:n, 0:n].astype(np.float64)
    # Coordinates in the 512 viewBox, the mark scaled about the centre.
    u = ((x + 0.5) / n * 512 - 256) / scale + 256
    v = ((y + 0.5) / n * 512 - 256) / scale + 256
    d = np.hypot(u - 256, v - 256)
    stroke = lambda r, w: np.clip(w / 2 - np.abs(d - r) + 0.5 * 512 / n / scale, 0, 1)
    ring = np.maximum(stroke(200, 34), stroke(146, 6) * 0.55)
    # Gradient along the diagonal from (0.08, 0.08) to (0.92, 0.92) of the box.
    t = np.clip(((u / 512 - 0.08) + (v / 512 - 0.08)) / (2 * 0.84), 0, 1)
    color = np.zeros((n, n, 3))
    for (a, ca), (b, cb) in zip(STOPS, STOPS[1:]):
        k = np.clip((t - a) / (b - a), 0, 1)[..., None]
        inside = ((t >= a) & (t <= b))[..., None]
        color = np.where(inside, np.array(ca) * (1 - k) + np.array(cb) * k, color)
    out = np.array(BG) * (1 - ring[..., None]) + color * ring[..., None]
    img = Image.fromarray(out.round().astype(np.uint8), "RGB")
    return img.resize((size, size), Image.LANCZOS)

render(192, 1.0).save("web/icon-192.png", optimize=True)
render(512, 1.0).save("web/icon-512.png", optimize=True)
render(512, 0.62).save("web/icon-maskable-512.png", optimize=True)
render(180, 1.0).save("web/apple-touch-icon.png", optimize=True)
