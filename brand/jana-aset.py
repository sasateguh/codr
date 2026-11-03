#!/usr/bin/env python3
"""jana-aset.py — menjana aset jenama dari brand/logo.svg dengan Chromium (Playwright):
apple-touch-icon.png (180, jubin penuh tanpa jidar), logo-512.png, logo-64.png, og.png (1200×630) dan
github-social.png (1280×640, pratonton sosial repo awam, dimuat naik tangan di Settings repo GitHub, D110).

    python3 brand/jana-aset.py <folder-keluaran>
Kemudian salin apple-touch-icon.png + og.png ke internal/web/static/ (hash statik berubah = URL baharu)."""
import os, sys, tempfile
from playwright.sync_api import sync_playwright

HERE = os.path.dirname(os.path.abspath(__file__))
FONTS = os.path.join(HERE, "..", "internal", "web", "static", "fonts")
svg = open(os.path.join(HERE, "logo.svg")).read()
out = sys.argv[1]
os.makedirs(out, exist_ok=True)

def html(body, bg="transparent"):
    return (f'<html><head><meta charset="utf-8"><style>'
            f'@font-face{{font-family:P;font-weight:600;src:url(file://{FONTS}/ibm-plex-sans-600.woff2)}}'
            f'@font-face{{font-family:P;font-weight:400;src:url(file://{FONTS}/ibm-plex-sans-400.woff2)}}'
            f'@font-face{{font-family:M;src:url(file://{FONTS}/ibm-plex-mono-400.woff2)}}'
            f'body{{margin:0;background:{bg};font-family:P,sans-serif;-webkit-font-smoothing:antialiased}}</style></head><body>{body}</body></html>')

def buka(pg, doc):
    # set_content = about:blank, yang TIDAK boleh memuat fon file:// (og.png lama jatuh ke fon sistem, D110): tulis ke fail, kemudian goto
    f = tempfile.NamedTemporaryFile("w", suffix=".html", delete=False); f.write(doc); f.close()
    pg.goto("file://" + f.name); pg.evaluate("document.fonts.ready"); pg.wait_for_timeout(300); os.unlink(f.name)

def kad(w, h, top, extra=""):
    return f'''<div style="position:relative;width:{w}px;height:{h}px;overflow:hidden;background:#0f1013;color:#e8e8e4">
      <div style="position:absolute;inset:0;background-image:linear-gradient(rgba(255,255,255,.035) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.035) 1px,transparent 1px);background-size:48px 48px"></div>
      <div style="position:absolute;left:88px;top:88px;display:flex;align-items:center;gap:22px">
        <div style="width:56px;height:56px">{svg.replace('width="256" height="256"', 'width="56" height="56"')}</div>
        <div style="font-size:26px;font-weight:600;letter-spacing:.06em">Codr</div>
      </div>
      <div style="position:absolute;left:88px;top:{top}px;right:88px">
        <div style="font-size:64px;font-weight:600;letter-spacing:-.03em;line-height:1.05">Kod anda, di mesin anda.</div>
        <div style="margin-top:14px;font-size:34px;font-weight:400;color:#9da1ab;letter-spacing:-.01em">Your code, on your own machine.</div>{extra}
      </div>
      <div style="position:absolute;left:88px;bottom:72px;right:88px;display:flex;gap:40px;font-family:M,monospace;font-size:18px;color:#9da1ab;border-top:1px solid #2a2c33;padding-top:22px">
        <span>git forge</span><span>SSH + HTTPS</span><span>BM + EN</span><span>AGPL-3.0</span><span style="margin-left:auto">Dibina di Malaysia</span>
      </div></div>'''

with sync_playwright() as p:
    b = p.chromium.launch()
    for size, name in [(180, "apple-touch-icon.png"), (512, "logo-512.png"), (64, "logo-64.png")]:
        pg = b.new_page(viewport={"width": size, "height": size}, device_scale_factor=1)
        inner = svg.replace('width="256" height="256"', f'width="{size}" height="{size}"')
        if name == "apple-touch-icon.png":  # iOS masks the icon: fill the canvas edge to edge
            inner = inner.replace('viewBox="0 0 256 256"', 'viewBox="16 16 224 224"')
        pg.set_content(html(inner)); pg.screenshot(path=os.path.join(out, name), omit_background=(name != "apple-touch-icon.png")); pg.close()
    # OG card + GitHub social preview: ink paper, the mark, the two taglines, a quiet spec line. No glow, no gradient.
    for w, h, name, top, extra in [(1200, 630, "og.png", 250, ""),
                                   (1280, 640, "github-social.png", 230, '<div style="margin-top:28px;font-family:M,monospace;font-size:22px">github.com/sasateguh/codr</div>')]:
        pg = b.new_page(viewport={"width": w, "height": h}, device_scale_factor=1)
        buka(pg, html(kad(w, h, top, extra), "#0f1013")); pg.screenshot(path=os.path.join(out, name)); pg.close()
    b.close()
print("ok", out)
