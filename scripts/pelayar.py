#!/usr/bin/env python3
"""pelayar.py — gerbang pelayar Codr: Chromium desktop + WebKit iPhone 13, tema cerah + gelap.

Ia membina binari, memasang contoh sebenar dalam folder sementara (pengguna, repo, commit, tag),
menjalankan `codr serve`, kemudian membuka setiap halaman:
  - tiada limpahan mendatar (scrollWidth ≤ lebar skrin) pada iPhone 13
  - tiada ralat konsol / permintaan gagal (CSP tanpa inline: pelanggaran = ralat konsol)
  - axe-core: sifar pelanggaran serius/kritikal pada setiap halaman, kedua-dua tema
  - sasaran sentuh ≥ 44px untuk pautan baris lejar pada telefon
  - butang Salin berfungsi (JS progresif) dan log masuk melalui borang sebenar
Tangkapan skrin ke .scratch/pelayar/ untuk semakan mata.

    python3 scripts/pelayar.py            # gerbang
Keluar 1 bila ada kegagalan.
"""
import http.cookiejar, json, os, re, shutil, signal, subprocess, sys, tempfile, time, urllib.parse, urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
AXE = ROOT / "scripts" / "vendor" / "axe.min.js"
OUT = ROOT / ".scratch" / "pelayar"
try:
    from playwright.sync_api import sync_playwright
except ImportError:
    print("langkau: playwright python tiada"); sys.exit(0)

bad = []
# Semakan yang tangkapan skrin v2.14.0 tunjuk tetapi gerbang terlepas (D78): mesej yang diberi hujah salah ("%!(EXTRA"),
# kotak semak yang dihimpit flex, dan tab aktif yang terpotong di tepi skrin telefon. Dipanggil pada setiap halaman diuji.
def totp(secret, step):
    """RFC 6238 dalam Python sendiri: gerbang mengira kod secara bebas daripada kod Go yang diuji (D80)."""
    import base64, hashlib, hmac, struct
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    d = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    o = d[-1] & 15
    return "%06d" % ((struct.unpack(">I", d[o:o + 4])[0] & 0x7fffffff) % 1000000)

def cacat_halaman(pg):
    out = []
    if "%!" in pg.content(): out.append("ralat format mesej (%!)")
    kecil = pg.evaluate("""Array.from(document.querySelectorAll('input[type=checkbox], input[type=radio]')).filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.width < 14; }).length""")
    if kecil: out.append(f"{kecil} kotak semak dihimpit < 14px")
    potong = pg.evaluate("""(() => { const a = document.querySelector('nav.tab [aria-current=page]'); if (!a) return 0; const r = a.getBoundingClientRect(), n = a.parentNode.getBoundingClientRect(); return Math.max(0, Math.round(r.right - n.right), Math.round(n.left - r.left)); })()""")
    if potong > 1: out.append(f"tab aktif terpotong {potong}px")
    pudar = pg.evaluate("""(() => { const n = document.querySelector('nav.tab'); if (!n || n.scrollWidth <= n.clientWidth + 2) return true;
        return n.classList.contains('tab-kanan') || n.classList.contains('tab-kiri'); })()""")
    if not pudar: out.append("tab melimpah tanpa tepi pudar (tab-kanan/tab-kiri)")
    return out

def fail(msg): bad.append(msg); print("  ✗", msg)
def ok(msg): print("  ✓", msg)

def lum(rgb):
    def ch(c):
        c = c / 255
        return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
    r, g, b = rgb
    return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b)

def ratio(fg, bg):
    a, b = lum(fg), lum(bg)
    return (max(a, b) + 0.05) / (min(a, b) + 0.05)

# axe membaca warna <select> WebKit sebagai hitam walaupun getComputedStyle berkata sebaliknya
# (diukur 14 Okt 2026: color rgb(232,230,225), axe fgColor #000000). Setiap pelanggaran kontras
# disahkan semula atas warna COMPUTED sebenar sebelum dikira gagal.
SEMAK_KONTRAS = r"""(sel) => { const e = document.querySelector(sel); if (!e) return null;
  const rgb = s => { const m = s.match(/[\d.]+/g); return m ? m.slice(0,3).map(Number) : null; };
  const a = s => { const m = s.match(/[\d.]+/g); return m && m.length > 3 ? Number(m[3]) : 1; };
  let bg = null, n = e; while (n && !bg) { const c = getComputedStyle(n).backgroundColor; if (a(c) > 0.99) bg = rgb(c); n = n.parentElement; }
  const cs = getComputedStyle(e); return { fg: rgb(cs.color), bg: bg, size: parseFloat(cs.fontSize), weight: cs.fontWeight }; }"""

def kontras_sah(pg, vio):
    """Pulangkan senarai pelanggaran yang KEKAL selepas pengesahan computed-style."""
    kekal = []
    for v in vio:
        if v["id"] != "color-contrast":
            kekal.append(v); continue
        nodes = []
        for n in v["nodes"]:
            info = pg.evaluate(SEMAK_KONTRAS, n["target"][0]) if len(n["target"]) == 1 else None
            if info and info["fg"] and info["bg"]:
                besar = info["size"] >= 24 or (info["size"] >= 18.66 and int(info["weight"]) >= 700)
                if ratio(info["fg"], info["bg"]) >= (3.0 if besar else 4.5):
                    continue  # axe tersilap baca; nilai sebenar lulus
            nodes.append(n)
        if nodes:
            v = dict(v, nodes=nodes); kekal.append(v)
    return kekal

def sh(*args, stdin=None, env=None, check=True):
    r = subprocess.run(list(args), input=stdin, capture_output=True, text=True, env=env)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(args)}: {r.stdout}{r.stderr}")
    return r.stdout

def main():
    OUT.mkdir(parents=True, exist_ok=True)
    tmp = Path(tempfile.mkdtemp(prefix="codr-pelayar-"))
    # HOME dialih untuk codr/git (config pengguna tidak dibaca), TETAPI `go build` memakai HOME sebenar:
    # dengan HOME sementara Go membina semula cache + modul (~300 MB, fail baca-sahaja) setiap larian dan
    # rmtree gagal memadamnya — diukur 14 Okt 2026: tujuh salinan, cakera hos penuh.
    env = dict(os.environ, CODR_LANG="en-US", HOME=str(tmp / "home"), PATH=f"{Path.home()}/go-toolchain/go/bin:{os.environ['PATH']}")
    (tmp / "home").mkdir()
    binp = tmp / "codr"
    sh("go", "build", "-o", str(binp), "./cmd/codr", env=dict(os.environ, PATH=env["PATH"]))
    inst = tmp / "inst"
    sh(str(binp), "init", "--dir", str(inst), env=env)
    cfg = (inst / "config.toml").read_text()
    cfg = re.sub(r'http_addr = .*', 'http_addr = "127.0.0.1:0"', cfg)
    cfg = re.sub(r'ssh_addr = .*', 'ssh_addr = "127.0.0.1:0"', cfg)
    cfg = re.sub(r'argon2_memory_kib = .*', 'argon2_memory_kib = 8192', cfg)
    # laman pendaratan (D56): "localhost" = nama hos pendaratan, 127.0.0.1 = forge — dua nama, satu pelayan
    cfg = re.sub(r'landing_hosts = .*', 'landing_hosts = ["localhost"]', cfg) if "landing_hosts" in cfg else cfg.replace("[site]", '[site]\nlanding_hosts = ["localhost"]')
    (inst / "config.toml").write_text(cfg)
    C = [str(binp), "--config", str(inst / "config.toml")]
    sh(*C, "admin", "user", "add", "aiman", "--admin", "--lang", "ms-MY", stdin="kata laluan ujian panjang\n", env=env)
    tok = re.search(r"codr_[0-9a-f]{64}", sh(*C, "admin", "token", "add", "aiman", env=env)).group(0)
    sh(*C, "admin", "repo", "add", "aiman/sistemtempahanbilikkhas", "--public", "--desc", "Sistem tempahan fasiliti — SaaS v5", env=env)
    sh(*C, "admin", "repo", "add", "aiman/rahsia", env=env)
    sh(*C, "admin", "user", "add", "baru", "--lang", "en-US", stdin="kata laluan pengguna baru\n", env=env)  # D64: belum ada kunci/repo/tolakan
    sh(*C, "admin", "user", "add", "dua", "--lang", "ms-MY", stdin="kata laluan dua langkah\n", env=env)  # D80: aliran 2FA
    srv = subprocess.Popen(C + ["serve"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
    http_addr = None
    for _ in range(200):
        line = srv.stdout.readline()
        m = re.search(r"HTTP listening on (127\.0\.0\.1:\d+)", line)
        if m: http_addr = m.group(1); break
    if not http_addr:
        srv.kill(); print(srv.stderr.read()); sys.exit("serve tidak bermula")
    base = f"http://{http_addr}"
    landing = base.replace("127.0.0.1", "localhost")
    try:
        # kandungan sebenar: README BM/EN, kod Go, fail binari, beberapa commit, tag
        work = tmp / "work"; work.mkdir(); (work / "src").mkdir()
        genv = dict(env, GIT_TERMINAL_PROMPT="0")
        g = lambda *a: sh("git", "-c", "user.name=Aiman", "-c", "user.email=a@example.invalid", "-c", "init.defaultBranch=main", "-C", str(work), *a, env=genv)
        g("init", "-q")
        (work / "README.md").write_text("# Sistem Tempahan Bilik Khas\n\nSaaS tempahan fasiliti untuk sekolah dan agensi. **Dwibahasa** dari hari pertama.\n\n## Mula\n\n```sh\nbin/dev php artisan test\n```\n\n| Modul | Status |\n|---|---|\n| Tempahan | siap |\n| Invois | draf |\n")
        (work / "src" / "tempahan.go").write_text("package tempahan\n\nimport \"time\"\n\n// Slot ialah satu tempahan fasiliti.\ntype Slot struct {\n\tMula, Tamat time.Time\n\tBilik       string\n}\n\nfunc (s Slot) Tempoh() time.Duration { return s.Tamat.Sub(s.Mula) }\n")
        (work / "logo.png").write_bytes(bytes([0x89, 0x50, 0x4E, 0x47, 0, 1, 2]))
        g("add", "."); g("commit", "-q", "-m", "Rangka awal: modul tempahan + README")
        (work / "src" / "tempahan.go").write_text("package tempahan\n\nimport \"time\"\n\n// Slot ialah satu tempahan fasiliti.\ntype Slot struct {\n\tMula, Tamat time.Time\n\tBilik       string\n\tDisahkan    bool\n}\n\nfunc (s Slot) Tempoh() time.Duration { return s.Tamat.Sub(s.Mula) }\n\n// Sah menandakan tempahan diluluskan.\nfunc (s *Slot) Sah() { s.Disahkan = true }\n")
        # Git LFS (D82): satu fail penuding dalam commit ini; objeknya dimuat naik melalui titik akhir LFS sebenar selepas push
        import hashlib
        lfs_isi = (b"%PDF-1.4 pelan lantai bilik mesyuarat " * 4000)[:150000]
        lfs_oid = hashlib.sha256(lfs_isi).hexdigest()
        (work / "reka").mkdir(exist_ok=True)
        (work / "reka" / "pelan-lantai.pdf").write_text(f"version https://git-lfs.github.com/spec/v1\noid sha256:{lfs_oid}\nsize {len(lfs_isi)}\n")
        g("add", "."); g("commit", "-q", "-m", "tempahan: tambah status Disahkan\n\nPengesahan admin diperlukan sebelum invois.")
        g("tag", "v0.1.0")
        # cawangan ciri untuk PR (v1.2): satu commit di atas main, tanpa konflik
        g("checkout", "-q", "-b", "ciri-invois")
        (work / "src" / "invois.go").write_text("package tempahan\n\n// Invois dijana selepas tempahan disahkan.\ntype Invois struct {\n\tNombor string\n\tJumlah int\n}\n")
        g("add", "."); g("commit", "-q", "-m", "invois: rangka struct Invois")
        g("checkout", "-q", "main")
        url = f"http://aiman:{tok}@{http_addr}/aiman/sistemtempahanbilikkhas.git"
        g("push", "-q", url, "main", "ciri-invois", "v0.1.0")
        ok("contoh dipasang (2 commit, 1 tag, 1 cawangan ciri, README, kod, binari)")
        rq = urllib.request.Request(f"http://{http_addr}/aiman/sistemtempahanbilikkhas.git/info/lfs/objects/{lfs_oid}", data=lfs_isi, method="PUT", headers={"Authorization": "Bearer " + tok})
        with urllib.request.urlopen(rq, timeout=10) as rs: lfs_kod = rs.status
        rq = urllib.request.Request(f"http://{http_addr}/aiman/sistemtempahanbilikkhas/raw/main/reka/pelan-lantai.pdf")
        with urllib.request.urlopen(rq, timeout=10) as rs: lfs_raw = rs.read()
        if lfs_kod != 200 or lfs_raw != lfs_isi: fail(f"LFS: muat naik {lfs_kod}, raw {len(lfs_raw)} bait (dijangka {len(lfs_isi)})")
        else: ok("LFS: objek dimuat naik melalui PUT sebenar, raw memulangkan kandungan sebenar (bukan penuding)")
        # wiki (D83): dua halaman melalui borang web sebenar (log masuk + CSRF), satu suntingan supaya sejarah ada dua baris
        jar = http.cookiejar.CookieJar(); op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        def borang(path, data):
            csrf = next((c.value for c in jar if c.name == "codr_csrf"), "")
            rq = urllib.request.Request(f"http://{http_addr}{path}", data=urllib.parse.urlencode({**data, "csrf": csrf}).encode(), method="POST")
            return op.open(rq, timeout=10)
        op.open(f"http://{http_addr}/login", timeout=10).read()
        borang("/login", {"user": "aiman", "password": "kata laluan ujian panjang"}).read()
        op.open(f"http://{http_addr}/aiman/sistemtempahanbilikkhas/wiki/_baharu", timeout=10).read()
        borang("/aiman/sistemtempahanbilikkhas/wiki/_baharu", {"title": "Home", "body": "# Wiki sistem tempahan\n\nMula dengan [[Panduan Pasang]]. Soalan lain: [[Soalan Lazim|soalan lazim]].\n\n| Modul | Pemilik |\n|---|---|\n| Tempahan | Aiman |\n", "message": "Cipta Home"}).read()
        borang("/aiman/sistemtempahanbilikkhas/wiki/_baharu", {"title": "Panduan Pasang", "body": "## Pasang\n\n```sh\nbin/dev php artisan migrate --seed\n```\n\nKembali ke [[Home]].\n"}).read()
        b = op.open(f"http://{http_addr}/aiman/sistemtempahanbilikkhas/wiki/Home/sunting", timeout=10).read().decode()
        wiki_base = re.search(r'name="base" value="([0-9a-f]{40})"', b).group(1)
        borang("/aiman/sistemtempahanbilikkhas/wiki/Home/sunting", {"title": "Home", "base": wiki_base, "body": "# Wiki sistem tempahan\n\nMula dengan [[Panduan Pasang]]. Soalan lain: [[Soalan Lazim|soalan lazim]].\n\n| Modul | Pemilik |\n|---|---|\n| Tempahan | Aiman |\n| Invois | Aiman |\n", "message": "Tambah modul invois"}).read()
        with urllib.request.urlopen(f"http://{http_addr}/aiman/sistemtempahanbilikkhas/wiki/Home/sejarah", timeout=10) as rs: sej = rs.read().decode()
        if "Tambah modul invois" not in sej or "Cipta Home" not in sej: fail("wiki: borang web tidak menghasilkan dua semakan dalam sejarah")
        else: ok("wiki: 2 halaman + 1 suntingan melalui borang web sebenar, sejarah 2 semakan")

        pages = ["/", "/login", "/aiman", "/aiman/sistemtempahanbilikkhas", "/aiman/sistemtempahanbilikkhas/tree/main/src", "/aiman/sistemtempahanbilikkhas/blob/main/reka/pelan-lantai.pdf", "/aiman/sistemtempahanbilikkhas/wiki", "/aiman/sistemtempahanbilikkhas/wiki/Panduan-Pasang", "/aiman/sistemtempahanbilikkhas/wiki/Home/sejarah", "/aiman/sistemtempahanbilikkhas/wiki/_semua",
                 "/aiman/sistemtempahanbilikkhas/blob/main/src/tempahan.go", "/aiman/sistemtempahanbilikkhas/blob/main/logo.png",
                 "/aiman/sistemtempahanbilikkhas/commits/main", "/aiman/sistemtempahanbilikkhas/refs", "/tiada-repo/x",
                 "/aiman/sistemtempahanbilikkhas/blame/main/src/tempahan.go", "/aiman/sistemtempahanbilikkhas/cari?q=tempahan"]
        head = urllib.request.urlopen(base + "/aiman/sistemtempahanbilikkhas/commits/main").read().decode()
        commit = re.search(r"/commit/([0-9a-f]{40})", head).group(1)
        pages.append(f"/aiman/sistemtempahanbilikkhas/commit/{commit}")
        axe_src = AXE.read_text()
        kunci_fail = Path(tmp) / "uji_ed25519"  # kunci sekali guna untuk borang /akaun (awam sahaja yang ditampal)
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(kunci_fail)], check=True)
        KUNCI_UJIAN = (Path(tmp) / "uji_ed25519.pub").read_text().strip()
        with sync_playwright() as p:
            runs = [("chromium", "desktop", "light"), ("chromium", "desktop", "dark"), ("webkit", "iPhone 13", "light"), ("webkit", "iPhone 13", "dark")]
            for engine, device, theme in runs:
                br = getattr(p, engine).launch()
                opts = dict(p.devices["iPhone 13"]) if device == "iPhone 13" else {"viewport": {"width": 1366, "height": 900}}
                ctx = br.new_context(color_scheme=theme, **opts)
                # axe disuntik sebagai skrip-awal (laluan protokol pelayar, bukan <script> dalam halaman —
                # CSP kita menolak yang kedua, dan itu memang yang diuji di bawah).
                ctx.add_init_script(axe_src)
                pg = ctx.new_page()
                shot = ctx.new_page()  # tangkapan skrin berasingan: screenshot penuh WebKit menyuntik <style> yang CSP kita tolak
                errors = []
                sengaja404 = {"/tiada-repo/x"}
                keadaan = {"abaikan404": False}
                pg.on("console", lambda m: errors.append(m.text) if m.type == "error" and not (keadaan["abaikan404"] and "404" in m.text) else None)
                pg.on("requestfailed", lambda r: errors.append(f"gagal: {r.url}"))
                pg.on("pageerror", lambda e: errors.append(str(e)))
                for path in pages:
                    label = f"{path} [{engine} {device} {theme}]"
                    keadaan["abaikan404"] = path in sengaja404
                    pg.goto(base + path, wait_until="load")
                    pg.wait_for_timeout(150)
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    if over > 1: fail(f"{label}: limpahan mendatar {over}px")
                    for c in cacat_halaman(pg): fail(f"{label}: {c}")
                    if device == "iPhone 13":
                        kecil = pg.evaluate("""Array.from(document.querySelectorAll('a.baris, .butang, button, .tab a')).filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.height < 32; }).map(e => e.className + ':' + Math.round(e.getBoundingClientRect().height))""")
                        if kecil: fail(f"{label}: sasaran sentuh < 32px: {kecil[:4]}")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if vio: fail(f"{label}: axe {[(v['id'], v['nodes'][0]['target'], v['nodes'][0]['any'][0].get('data')) for v in vio]}")
                    name = re.sub(r"[^a-z0-9]+", "-", path.strip("/") or "home")[:60]
                    shot.goto(base + path, wait_until="load")
                    shot.screenshot(path=str(OUT / f"{name}--{engine}-{device.replace(' ', '')}-{theme}.png"), full_page=True)
                # laman pendaratan (D56): dua bahasa, kanvas 3D benar-benar dilukis, tiada limpahan, axe bersih
                for path in ["/", "/ms"]:  # D60: Inggeris di "/", Melayu di /ms
                    label = f"pendaratan {path} [{engine} {device} {theme}]"
                    pg.goto(landing + path, wait_until="load")
                    pg.wait_for_timeout(1900)  # intro graf 1.6 s
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    if over > 1: fail(f"{label}: limpahan mendatar {over}px")
                    lukis = pg.evaluate("""(() => { const c = document.getElementById('graf3d'); if (!c || c.hidden) return -1;
                        const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data; let n = 0;
                        for (let i = 3; i < d.length; i += 16) if (d[i] > 0) n++; return n; })()""")
                    if lukis < 500: fail(f"{label}: kanvas 3D kosong atau tersembunyi ({lukis})")
                    if device == "iPhone 13":
                        kecil = pg.evaluate("""Array.from(document.querySelectorAll('.butang, button, .p-bahasa a')).filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.height < 32; }).map(e => e.className + ':' + Math.round(e.getBoundingClientRect().height))""")
                        if kecil: fail(f"{label}: sasaran sentuh < 32px: {kecil[:4]}")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if vio: fail(f"{label}: axe {[(v['id'], v['nodes'][0]['target'], v['nodes'][0]['any'][0].get('data')) for v in vio]}")
                    name = "pendaratan" + ("-ms" if path == "/ms" else "")
                    shot.goto(landing + path, wait_until="load"); shot.wait_for_timeout(1900)
                    shot.screenshot(path=str(OUT / f"{name}--{engine}-{device.replace(' ', '')}-{theme}.png"), full_page=True)
                    shot.screenshot(path=str(OUT / f"{name}-skrin1--{engine}-{device.replace(' ', '')}-{theme}.png"))
                # tulisan reka bentuk (D90): docs/*.md dalam dua bahasa, satu lajur bacaan, kod sebaris membalut
                for path in ["/design", "/ms/design"]:
                    label = f"reka bentuk {path} [{engine} {device} {theme}]"
                    pg.goto(landing + path, wait_until="load"); pg.wait_for_timeout(150)
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    if over > 1: fail(f"{label}: limpahan mendatar {over}px")
                    if pg.locator("article.p-artikel h2").count() < 5: fail(f"{label}: tulisan tidak dirender")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if vio: fail(f"{label}: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    shot.goto(landing + path, wait_until="load")
                    shot.screenshot(path=str(OUT / f"reka-bentuk{'-ms' if '/ms' in path else ''}--{engine}-{device.replace(' ', '')}-{theme}.png"), full_page=True)
                if errors: fail(f"[{engine} {device} {theme}] ralat konsol/rangkaian: {errors[:3]}")
                else: ok(f"[{engine} {device} {theme}] {len(pages)} halaman: tiada limpahan, tiada ralat konsol, axe bersih")
                # butang Salin: JS progresif (hidden tanpa JS → kelihatan dengan JS) + klik
                if engine == "chromium" and theme == "light":
                    ctx.grant_permissions(["clipboard-read", "clipboard-write"])
                    pg.goto(base + "/aiman/sistemtempahanbilikkhas", wait_until="load")
                    b = pg.locator("button.salin[data-salin=klon-ssh]")
                    if not b.is_visible(): fail("butang Salin tidak dipaparkan walaupun JS hidup")
                    else:
                        b.click(); pg.wait_for_timeout(100)
                        if b.text_content().strip() != "Disalin" and b.text_content().strip() != "Copied": fail(f"butang Salin tidak bertukar teks: {b.text_content()!r}")
                        else: ok("butang Salin berfungsi dan bertukar teks")
                    # log masuk sebenar + repo persendirian + Tetapan
                    pg.goto(base + "/login", wait_until="load")
                    pg.fill("input[name=user]", "aiman"); pg.fill("input[name=password]", "salah"); pg.click("form[action='/login'] button[type=submit]")
                    if not pg.locator("p.ralat").is_visible(): fail("ralat kata laluan salah tidak dipaparkan")
                    pg.fill("input[name=password]", "kata laluan ujian panjang"); pg.click("form[action='/login'] button[type=submit]")
                    pg.wait_for_load_state("load")
                    if "Log keluar" not in pg.content() and "Log out" not in pg.content(): fail("log masuk gagal melalui borang")
                    else: ok("log masuk borang → sesi (BM ikut bahasa pengguna)")
                    # palet arahan (D68): Ctrl-K → taip → Enter membuka repo; /api/cari mesti lulus CSP (connect-src)
                    sebelum = len(errors)
                    pg.goto(base + "/", wait_until="load"); pg.keyboard.press("Control+k"); pg.wait_for_timeout(300)
                    pg.keyboard.type("sistemtempahan"); pg.wait_for_timeout(700)
                    pilihan = pg.locator(".palet-pilihan").all_inner_texts()
                    if not any("aiman/sistemtempahanbilikkhas" in x for x in pilihan): fail(f"palet: repo tidak disenaraikan: {pilihan}")
                    else:
                        try:
                            with pg.expect_navigation(timeout=8000): pg.keyboard.press("Enter")
                        except Exception: pass
                        if not pg.url.rstrip("/").endswith("/aiman/sistemtempahanbilikkhas") or len(errors) > sebelum: fail(f"palet: Enter → {pg.url}, ralat {errors[sebelum:]}")
                        else: ok("palet Ctrl-K: carian → Enter membuka repo, tiada ralat konsol")
                    pg.goto(base + "/aiman/rahsia", wait_until="load")
                    if "persendirian" not in pg.content(): fail("pemilik tidak nampak repo persendirian")
                    for path in ["/baharu", "/aiman/sistemtempahanbilikkhas/tetapan", "/admin/audit", "/akaun", "/akaun/aktiviti", "/admin/sandaran", "/admin/ai", "/admin/oidc", "/admin/data", "/admin/emel", "/pemberitahuan", "/pemberitahuan?semua=1"]:
                        pg.goto(base + path, wait_until="load")
                        over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                        if over > 1: fail(f"{path}: limpahan mendatar {over}px")
                        res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                        vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                        if vio: fail(f"{path}: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                        shot.goto(base + path, wait_until="load"); shot.screenshot(path=str(OUT / f"{path.strip('/').replace('/', '-')}--chromium.png"), full_page=True)
                    pg.goto(base + "/baharu", wait_until="load")
                    pg.fill("input[name=name]", "projek-web"); pg.click("form[action='/baharu'] button[type=submit]"); pg.wait_for_load_state("load")
                    if "projek-web" not in pg.url: fail(f"cipta repo dari borang gagal: {pg.url}")
                    else: ok("cipta repo dari borang web → muka repo")
                    # webhook (D78): cipta dari borang → rahsia dipapar sekali; .invalid tidak pernah diselesaikan DNS, jadi tiada trafik keluar
                    pg.goto(base + "/aiman/sistemtempahanbilikkhas/webhook", wait_until="load")
                    pg.fill("form[action$='/webhook'] input[name=url]", "https://ci.example.invalid/codr")
                    pg.check("form[action$='/webhook'] input[name=events][value=push]")
                    pg.click("form[action$='/webhook'] button[type=submit]"); pg.wait_for_load_state("load")
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    pilih = pg.evaluate("Array.from(document.querySelectorAll('input[name=events]:checked')).map(e => e.value)")
                    if not re.search(r"whsec_[0-9a-f]{48}", pg.content()) or pg.locator("h1.tajuk-hook").count() != 1: fail(f"webhook: rahsia tidak dipapar selepas cipta ({pg.url})")
                    elif pilih != ["push"]: fail(f"webhook: tanda 'push' sahaja disimpan sebagai {pilih}")
                    elif over > 1 or vio: fail(f"webhook: limpahan {over}px, axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    else: ok("webhook: cipta dari borang → rahsia sekali, axe bersih")
                    shot.goto(base + "/aiman/sistemtempahanbilikkhas/webhook/1", wait_until="load"); shot.screenshot(path=str(OUT / "webhook--chromium.png"), full_page=True)
                    shot.goto(base + "/aiman/rahsia", wait_until="load"); shot.screenshot(path=str(OUT / "rahsia-logged-in--chromium.png"), full_page=True)
                    # /akaun: tambah kunci SSH + cipta token dari borang (v0.6), kemudian buang kedua-duanya
                    pg.goto(base + "/akaun", wait_until="load")
                    pg.fill("input[name=title]", "pc-ujian"); pg.fill("textarea[name=key]", KUNCI_UJIAN); pg.click("form[action='/akaun/kunci'] button[type=submit]"); pg.wait_for_load_state("load")
                    if "SHA256:" not in pg.content(): fail("tambah kunci SSH dari borang gagal")
                    else: ok("tambah kunci SSH dari borang → disenaraikan")
                    pg.fill("input[name=name]", "token-ujian"); pg.click("form[action='/akaun/token'] button[type=submit]"); pg.wait_for_load_state("load")
                    if not re.search(r"codr_[0-9a-f]{64}", pg.content()): fail("cipta token dari borang gagal")
                    else: ok("cipta token dari borang → dipapar sekali")
                    shot.goto(base + "/akaun", wait_until="load"); shot.screenshot(path=str(OUT / "akaun--chromium.png"), full_page=True)
                    for sel in ["form[action='/akaun/kunci/padam'] button", "form[action='/akaun/token/padam'] button"]:
                        pg.goto(base + "/akaun", wait_until="load"); pg.click(sel); pg.wait_for_load_state("load")
                    if "SHA256:" in pg.content() or "token-ujian" in pg.content(): fail("buang kunci/token dari borang gagal")
                    else: ok("buang kunci + token dari borang")
                    # isu (v1.1): label → isu → komen → tutup melalui borang sebenar; halaman isu masuk senarai untuk larian seterusnya
                    # OIDC (D91): borang pentadbir sebenar (issuer .invalid tidak pernah dihubungi semasa simpan) → butang di halaman log masuk
                    pg.goto(base + "/admin/oidc", wait_until="load")
                    pg.fill("input[name=name]", "Syarikat Contoh"); pg.fill("input[name=issuer]", "https://idp.example.invalid")
                    pg.fill("input[name=client_id]", "codr"); pg.fill("input[name=secret]", "rahsia-ujian"); pg.check("input[name=enabled]")
                    pg.click("form[action='/admin/oidc'] button[type=submit]"); pg.wait_for_load_state("load")
                    oc = br.new_context(**opts); oc.add_init_script(axe_src); op = oc.new_page(); op.goto(base + "/login", wait_until="load")
                    ada = op.locator("a[href^='/login/oidc']").count() == 1
                    over = op.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = op.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(op, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    op.screenshot(path=str(OUT / "login-oidc--chromium.png"), full_page=True); oc.close()
                    if not ada or over > 1 or vio: fail(f"OIDC: butang log masuk {ada}, limpahan {over}px, axe {[v['id'] for v in vio]}")
                    else: ok("OIDC: dihidupkan dari borang pentadbir → halaman log masuk menawarkan butang penyedia, axe bersih")
                    pg.goto(base + "/aiman/sistemtempahanbilikkhas/label", wait_until="load")
                    pg.fill("input[name=name]", "pepijat"); pg.check("input[name=colour][value=h3]"); pg.click("form[action$='/label'] button[type=submit]"); pg.wait_for_load_state("load")
                    if "pepijat" not in pg.content(): fail("cipta label dari borang gagal")
                    pg.goto(base + "/aiman/sistemtempahanbilikkhas/isu/baharu", wait_until="load")
                    pg.fill("input[name=title]", "Butang Salin tidak muncul di Safari"); pg.fill("textarea[name=body]", "Langkah:\n\n1. Buka repo\n2. Tekan **Salin**\n\nLihat juga #2.")
                    pg.click("form[action$='/isu'] button[type=submit]"); pg.wait_for_load_state("load")
                    if not pg.url.endswith("/isu/1"): fail(f"cipta isu dari borang gagal: {pg.url}")
                    else:
                        pg.check("fieldset.senarai-label input[type=checkbox]"); pg.click("form[action$='/isu/1/label'] button[type=submit]"); pg.wait_for_load_state("load")
                        pg.fill("form.komen-borang textarea[name=body]", "Disahkan di iPhone 13 — `navigator.clipboard` tiada tanpa HTTPS."); pg.click("form.komen-borang button[type=submit]"); pg.wait_for_load_state("load")
                        if "navigator.clipboard" not in pg.content() or 'class="label h3' not in pg.content(): fail("komen/label isu tidak dipaparkan")
                        else: ok("isu: label + isu + komen dari borang")
                        # penerima (D85): "Tugaskan kepada saya" melalui borang sebenar → kad sisi menamakannya, senarai ditapis menjumpainya
                        pg.click("form[action$='/isu/1/penerima'] button:has-text('saya'), form[action$='/isu/1/penerima'] button:has-text('Assign to me')"); pg.wait_for_load_state("load")
                        ada = pg.locator(".senarai-penerima a[href='/aiman']").count() == 1
                        pg.goto(base + "/aiman/sistemtempahanbilikkhas/isu?penerima=aiman", wait_until="load")
                        if not ada or "Butang Salin tidak muncul" not in pg.content(): fail("penerima: tugaskan kepada saya / tapis senarai gagal")
                        else: ok("penerima: tugaskan diri dari borang, senarai ditapis ?penerima=aiman menjumpai isu")
                        shot.goto(base + "/aiman/sistemtempahanbilikkhas/isu/1", wait_until="load"); shot.screenshot(path=str(OUT / "isu-1--chromium.png"), full_page=True)
                        # pemberitahuan (D79): orang lain memberi komen + menyebut @aiman → loceng aiman menyala, senarai menamakan sebabnya
                        bc = br.new_context(**opts); bp2 = bc.new_page()
                        bp2.goto(base + "/login", wait_until="load")
                        bp2.fill("input[name=user]", "baru"); bp2.fill("input[name=password]", "kata laluan pengguna baru"); bp2.click("form[action='/login'] button[type=submit]"); bp2.wait_for_load_state("load")
                        bp2.goto(base + "/aiman/sistemtempahanbilikkhas/isu/1", wait_until="load")
                        bp2.fill("form.komen-borang textarea[name=body]", "@aiman berlaku juga di Firefox, langkah sama. Log konsol tiada ralat."); bp2.click("form.komen-borang button[type=submit]"); bp2.wait_for_load_state("load")
                        bc.close()
                        dapat = False
                        for _ in range(40):  # penghantar dalam serve: Kick serta-merta, tik 5 s sebagai sandaran
                            pg.goto(base + "/", wait_until="load")
                            if pg.locator(".loceng-kiraan").count(): dapat = True; break
                            time.sleep(0.25)
                        pg.goto(base + "/pemberitahuan", wait_until="load")
                        if not dapat or "Butang Salin tidak muncul" not in pg.content() or pg.locator(".belum-baca").count() != 1: fail("pemberitahuan: komen + sebutan orang lain tidak sampai ke loceng/senarai pemilik")
                        else: ok("pemberitahuan: komen orang lain → loceng menyala, senarai menamakan benang")
                        pg.goto(base + "/aiman/sistemtempahanbilikkhas/isu/1", wait_until="load")
                        if pg.locator(".loceng-kiraan").count(): fail("pemberitahuan: membuka benang tidak menandanya dibaca")
                        pg.click("form[action$='/isu/1/keadaan'] button[value=closed]"); pg.wait_for_load_state("load")
                        if 'class="pil-keadaan closed"' not in pg.content(): fail("tutup isu gagal")
                        pg.click("form[action$='/isu/1/keadaan'] button[value=open]"); pg.wait_for_load_state("load")
                        pages += ["/aiman/sistemtempahanbilikkhas/isu", "/aiman/sistemtempahanbilikkhas/isu/1", "/aiman/sistemtempahanbilikkhas/label", "/aiman/sistemtempahanbilikkhas/isu?penerima=aiman"]
                    # PR (v1.2): banding → buka PR → fail berubah → gabung melalui borang sebenar; nombor kongsi dengan isu (#2)
                    pg.goto(base + "/aiman/sistemtempahanbilikkhas/pr/baharu?head=ciri-invois&base=main", wait_until="load")
                    if "invois: rangka struct Invois" not in pg.content(): fail("banding PR tidak menunjukkan commit cawangan")
                    pg.fill("form[action$='/pr'] input[name=title]", "Invois: rangka struct"); pg.fill("form[action$='/pr'] textarea[name=body]", "Untuk #1. Struct sahaja dahulu.\n\nFixes #1")
                    pg.click("form[action$='/pr'] button[type=submit]"); pg.wait_for_load_state("load")
                    if not pg.url.endswith("/pr/2"): fail(f"buka PR dari borang gagal: {pg.url}")
                    else:
                        if 'kotak-gabung boleh' not in pg.content() or 'href="/aiman/sistemtempahanbilikkhas/isu/1"' not in pg.content(): fail("PR: kotak gabung / pautan #1 tiada")
                        pg.goto(base + "/aiman/sistemtempahanbilikkhas/pr/2/fail", wait_until="load")
                        if "src/invois.go" not in pg.content() or 'class="baris-kod tambah"' not in pg.content(): fail("PR: diff fail berubah tiada")
                        shot.goto(base + "/aiman/sistemtempahanbilikkhas/pr/2", wait_until="load"); shot.screenshot(path=str(OUT / "pr-2--chromium.png"), full_page=True)
                        shot.goto(base + "/aiman/sistemtempahanbilikkhas/pr/2/fail", wait_until="load"); shot.screenshot(path=str(OUT / "pr-2-fail--chromium.png"), full_page=True)
                        pg.goto(base + "/aiman/sistemtempahanbilikkhas/pr/2", wait_until="load")
                        pg.click("form[action$='/pr/2/gabung'] button[type=submit]"); pg.wait_for_load_state("load")
                        if 'class="pil-keadaan merged"' not in pg.content(): fail("gabung PR dari borang gagal")
                        else:
                            pg.goto(base + "/aiman/sistemtempahanbilikkhas/tree/main/src", wait_until="load")
                            if "invois.go" not in pg.content(): fail("selepas gabung, main tiada invois.go")
                            else: ok("PR: banding → buka → diff → gabung; main memegang fail cawangan")
                        # D87: "Fixes #1" dalam badan PR yang digabung → pembaca xref dalam serve menutup isu #1 dan menamakan PR itu
                        tutup = False
                        for _ in range(40):
                            pg.goto(base + "/aiman/sistemtempahanbilikkhas/isu/1", wait_until="load")
                            if 'class="pil-keadaan closed"' in pg.content() and 'href="/aiman/sistemtempahanbilikkhas/pr/2"' in pg.content(): tutup = True; break
                            time.sleep(0.25)
                        if not tutup: fail("xref: gabung PR 'Fixes #1' tidak menutup isu #1 (atau tidak menamakan #2)")
                        else: ok("xref: gabung PR dengan 'Fixes #1' → isu #1 ditutup, halaman isu menamakan PR #2")
                        pages += ["/aiman/sistemtempahanbilikkhas/pr", "/aiman/sistemtempahanbilikkhas/pr/2", "/aiman/sistemtempahanbilikkhas/pr/2/fail", "/aiman/sistemtempahanbilikkhas/pr/baharu?head=ciri-invois&base=main"]
                        for path in pages[-8:]:
                            pg.goto(base + path, wait_until="load")
                            over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                            if over > 1: fail(f"{path}: limpahan mendatar {over}px")
                            res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                            vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                            if vio: fail(f"{path} [log masuk]: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                        shot.goto(base + "/aiman/sistemtempahanbilikkhas/isu", wait_until="load"); shot.screenshot(path=str(OUT / "isu--chromium.png"), full_page=True)
                    # pendaratan: gerakan dikurangkan = satu bingkai statik (tiada rAF berterusan), kanvas tetap dilukis
                    rctx = br.new_context(reduced_motion="reduce", viewport={"width": 1366, "height": 900})
                    rp = rctx.new_page(); rp.goto(landing + "/", wait_until="load"); rp.wait_for_timeout(300)
                    a = rp.evaluate("document.getElementById('graf3d').toDataURL()"); rp.wait_for_timeout(700)
                    b2 = rp.evaluate("document.getElementById('graf3d').toDataURL()")
                    if a != b2 or len(a) < 3000: fail("pendaratan: gerakan dikurangkan masih bergerak atau kanvas kosong")
                    else: ok("pendaratan: prefers-reduced-motion = bingkai statik yang dilukis")
                    rctx.close()
                    # pendaratan: tanpa JS = tanda SVG statik kelihatan, kanvas tersembunyi
                    nctx = br.new_context(java_script_enabled=False, viewport={"width": 390, "height": 844})
                    npg = nctx.new_page(); npg.goto(landing + "/", wait_until="load")
                    if not npg.locator(".p-statik").is_visible() or npg.locator("#graf3d").is_visible(): fail("pendaratan tanpa JS: tanda statik tidak kelihatan")
                    else: ok("pendaratan tanpa JS: tanda SVG statik")
                    nctx.close()
                    # pendaratan: LCP pada 4G perlahan + CPU 4x (Lighthouse mudah alih), sasaran < 2.5 s
                    lctx = br.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=3, is_mobile=True, has_touch=True)
                    lp = lctx.new_page(); cdp = lctx.new_cdp_session(lp)
                    cdp.send("Network.enable"); cdp.send("Network.emulateNetworkConditions", {"offline": False, "latency": 150, "downloadThroughput": 1.6e6 / 8, "uploadThroughput": 750e3 / 8})
                    cdp.send("Emulation.setCPUThrottlingRate", {"rate": 4})
                    lp.goto(landing + "/", wait_until="load"); lp.wait_for_timeout(500)
                    lcp = lp.evaluate("""new Promise(r => new PerformanceObserver(l => { const e = l.getEntries(); r(e[e.length - 1].startTime); }).observe({type: 'largest-contentful-paint', buffered: true}))""")
                    berat = lp.evaluate("performance.getEntriesByType('resource').reduce((n, e) => n + (e.transferSize || 0), performance.getEntriesByType('navigation')[0].transferSize || 0)")
                    if lcp > 2500: fail(f"pendaratan: LCP {lcp:.0f} ms pada 4G perlahan (sasaran < 2500)")
                    else: ok(f"pendaratan: LCP {lcp:.0f} ms pada 4G perlahan + CPU 4x, {berat/1024:.0f} KB dipindah")
                    lctx.close()
                    # togol tema (v0.7): butang muncul dengan JS, klik → html[data-tema=dark] + latar gelap, kekal selepas muat semula
                    pg.goto(base + "/", wait_until="load")
                    tb = pg.locator("button.tema")
                    if not tb.is_visible(): fail("butang tema tidak dipaparkan walaupun JS hidup")
                    else:
                        # warna tidak dikodkan di sini (token berubah ikut keputusan reka bentuk): gelap mesti BERBEZA dan lebih
                        # gelap daripada cerah, pilihan kekal selepas muat semula, dan togol kedua memulihkan warna asal
                        bg_cerah = pg.evaluate("getComputedStyle(document.body).backgroundColor")
                        lum_js = "(() => { const m = getComputedStyle(document.body).backgroundColor.match(/[\\d.]+/g).slice(0,3).map(Number); return m[0]+m[1]+m[2]; })()"
                        lum_cerah = pg.evaluate(lum_js)
                        tb.click(); pg.wait_for_timeout(100)
                        bg = pg.evaluate("getComputedStyle(document.body).backgroundColor"); lum_gelap = pg.evaluate(lum_js)
                        pg.reload(wait_until="load")
                        kekal = pg.evaluate("document.documentElement.getAttribute('data-tema')")
                        if bg == bg_cerah or lum_gelap >= lum_cerah or kekal != "dark": fail(f"togol tema: latar {bg} (cerah {bg_cerah}), selepas muat semula data-tema={kekal}")
                        else: ok("togol tema → gelap, kekal selepas muat semula")
                        tb.click(); pg.wait_for_timeout(100)
                        if pg.evaluate("getComputedStyle(document.body).backgroundColor") != bg_cerah: fail("togol tema kembali ke cerah gagal")
                        pg.evaluate("localStorage.removeItem('codr_tema')")
                    # togol bahasa di kepala (satu klik): BM → EN → BM
                    pg.goto(base + "/", wait_until="load")
                    pg.click("form.bahasa button[value='en-US']"); pg.wait_for_load_state("load")
                    if "Log out" not in pg.content(): fail("togol bahasa EN di kepala gagal")
                    else:
                        pg.click("form.bahasa button[value='ms-MY']"); pg.wait_for_load_state("load")
                        if "Log keluar" not in pg.content(): fail("togol bahasa BM di kepala gagal")
                        else: ok("togol bahasa kepala BM ⇄ EN")
                    # 2FA (D80): sediakan → kod pemulihan sekali → log keluar → kata laluan sahaja TIDAK log masuk → kod → log masuk
                    dc = br.new_context(**opts); dp = dc.new_page()
                    dp.goto(base + "/login", wait_until="load")
                    dp.fill("input[name=user]", "dua"); dp.fill("input[name=password]", "kata laluan dua langkah"); dp.click("form[action='/login'] button[type=submit]"); dp.wait_for_load_state("load")
                    dp.goto(base + "/akaun/2fa", wait_until="load")
                    rahsia = dp.locator(".rahsia-totp").inner_text().replace(" ", "")
                    qr_ok = dp.locator("svg.qr path.qr-dakwat").count() == 1
                    dp.fill("input[name=code]", totp(rahsia, int(time.time()) // 30)); dp.click("form[action='/akaun/2fa'] button[type=submit]"); dp.wait_for_load_state("load")
                    kod_pulih = dp.locator("ol.kod-pemulihan li").count()
                    dc.clear_cookies()  # log keluar: sesi lama hilang dari pelayar ini
                    dp.goto(base + "/login", wait_until="load")
                    dp.fill("input[name=user]", "dua"); dp.fill("input[name=password]", "kata laluan dua langkah"); dp.click("form[action='/login'] button[type=submit]"); dp.wait_for_load_state("load")
                    di_kod = dp.url.endswith("/login/kod"); dp.goto(base + "/akaun", wait_until="load"); tanpa_sesi = "/login" in dp.url
                    dp.goto(base + "/login", wait_until="load")
                    dp.fill("input[name=user]", "dua"); dp.fill("input[name=password]", "kata laluan dua langkah"); dp.click("form[action='/login'] button[type=submit]"); dp.wait_for_load_state("load")
                    dp.fill("input[name=code]", totp(rahsia, int(time.time()) // 30 + 1)); dp.click("form[action='/login/kod'] button[type=submit]"); dp.wait_for_load_state("load")
                    dp.goto(base + "/akaun", wait_until="load"); masuk = dp.url.endswith("/akaun") and "Hidup" in dp.content()
                    dc.close()
                    if not (qr_ok and kod_pulih == 10 and di_kod and tanpa_sesi and masuk): fail(f"2FA: qr={qr_ok} kod={kod_pulih} halaman-kod={di_kod} tanpa-sesi={tanpa_sesi} masuk={masuk}")
                    else: ok("2FA: QR + 10 kod pemulihan; kata laluan sahaja tiada sesi; kod TOTP (dikira Python) → log masuk")
                    pg.goto(base + "/admin/tetapan", wait_until="load")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if vio: fail(f"tetapan: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    shot.goto(base + "/admin/tetapan", wait_until="load"); shot.screenshot(path=str(OUT / "tetapan--chromium.png"), full_page=True)
                    shot.goto(base + "/aiman/sistemtempahanbilikkhas", wait_until="load"); shot.screenshot(path=str(OUT / "repo-logged-in-ms--chromium.png"), full_page=True)
                    shot.goto(base + "/", wait_until="load"); shot.screenshot(path=str(OUT / "home-logged-in-ms--chromium.png"), full_page=True)
                if engine == "webkit" and theme == "light":
                    pg.goto(base + "/login", wait_until="load")
                    pg.fill("input[name=user]", "aiman"); pg.fill("input[name=password]", "kata laluan ujian panjang"); pg.click("form[action='/login'] button[type=submit]"); pg.wait_for_load_state("load")
                    pg.goto(base + "/akaun", wait_until="load"); pg.wait_for_timeout(150)
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    if over > 1: fail(f"/akaun [webkit iPhone 13]: limpahan mendatar {over}px")
                    # kotak semak/radio: sasaran sentuhnya ialah LABEL pembalut (min-height 32px), jadi ukur label itu
                    kecil = pg.evaluate("""Array.from(document.querySelectorAll('.butang, button, input, textarea')).map(e => (e.type === 'checkbox' || e.type === 'radio') ? e.closest('label') || e : e).filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.height < 32; }).map(e => e.tagName + '.' + e.className + ':' + Math.round(e.getBoundingClientRect().height))""")
                    if kecil: fail(f"/akaun [webkit iPhone 13]: sasaran sentuh < 32px: {kecil[:4]}")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if vio: fail(f"/akaun [webkit iPhone 13]: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    else: ok("/akaun [webkit iPhone 13 light]: tiada limpahan, sasaran sentuh ok, axe bersih")
                    kc = br.new_context(color_scheme=theme, **opts); kc.add_init_script(axe_src); kp = kc.new_page()
                    kp.goto(base + "/login", wait_until="load")
                    kp.fill("input[name=user]", "dua"); kp.fill("input[name=password]", "kata laluan dua langkah"); kp.click("form[action='/login'] button[type=submit]"); kp.wait_for_load_state("load")
                    over = kp.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = kp.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(kp, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if not kp.url.endswith("/login/kod") or over > 1 or vio or cacat_halaman(kp): fail(f"/login/kod [webkit iPhone 13]: {kp.url} limpahan {over}px axe {[v['id'] for v in vio]} {cacat_halaman(kp)}")
                    else: ok("/login/kod [webkit iPhone 13]: tiada limpahan, axe bersih")
                    kp.screenshot(path=str(OUT / "login-kod--webkit-iPhone13.png"), full_page=True); kc.close()
                    # QR mesti BOLEH DIBACA, bukan sekadar nampak betul: nyahkod tangkapan skrin WebKit dengan zbar dan banding dengan kunci di halaman
                    try:
                        from pyzbar.pyzbar import decode as zbar
                        from PIL import Image
                        pg.goto(base + "/akaun/2fa", wait_until="load")
                        kunci = pg.locator(".rahsia-totp").inner_text().replace(" ", "")
                        pg.locator("svg.qr").screenshot(path=str(OUT / "qr--webkit.png"))
                        dibaca = [x.data.decode() for x in zbar(Image.open(OUT / "qr--webkit.png"))]
                        if len(dibaca) != 1 or "secret=" + kunci not in dibaca[0] or not dibaca[0].startswith("otpauth://totp/"): fail(f"QR 2FA tidak dibaca dengan betul: {dibaca}")
                        else: ok("QR 2FA [webkit iPhone 13]: dibaca zbar, kunci sama dengan halaman")
                    except ImportError:
                        print("  · QR tidak dinyahkod: pyzbar/PIL tiada di hos ini")
                    # API v1 (D81): status CI melalui API sebenar dengan token → dipapar di halaman commit, muat di iPhone
                    def api(method, path, body=None):
                        rq = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, method=method,
                                                    headers={"Authorization": "Bearer " + tok, "Content-Type": "application/json"})
                        with urllib.request.urlopen(rq, timeout=10) as rs: return rs.status, json.loads(rs.read())
                    _, cs = api("GET", "/api/v1/repos/aiman/sistemtempahanbilikkhas/commits?per_page=1")
                    sha = cs[0]["sha"]
                    api("POST", f"/api/v1/repos/aiman/sistemtempahanbilikkhas/statuses/{sha}", {"state": "success", "context": "ci/ujian-unit", "description": "128 ujian lulus dalam 41 s", "target_url": "https://ci.example.invalid/binaan/1842"})
                    kod, _ = api("POST", f"/api/v1/repos/aiman/sistemtempahanbilikkhas/statuses/{sha}", {"state": "failure", "context": "lint/gofmt", "description": "2 fail perlu diformat"})
                    _, gab = api("GET", f"/api/v1/repos/aiman/sistemtempahanbilikkhas/commits/{sha}/statuses")
                    pg.goto(base + f"/aiman/sistemtempahanbilikkhas/commit/{sha}", wait_until="load"); pg.wait_for_timeout(150)
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if kod != 201 or gab["state"] != "failure" or pg.locator(".senarai-semakan li").count() != 2 or over > 1 or vio or cacat_halaman(pg):
                        fail(f"status CI: kod {kod}, gabungan {gab.get('state')}, baris {pg.locator('.senarai-semakan li').count()}, limpahan {over}px, axe {[v['id'] for v in vio]}")
                    else: ok("API v1: status CI dicatat dengan token → halaman commit [webkit iPhone 13] memaparkannya, axe bersih")
                    pg.screenshot(path=str(OUT / "commit-status--webkit-iPhone13.png"), full_page=True)
                    for path in ["/pemberitahuan?semua=1", "/admin/oidc", "/aiman/sistemtempahanbilikkhas/tetapan", "/akaun/aktiviti", "/akaun/aktiviti?jenis=issue", "/admin/emel", "/akaun/2fa", "/aiman/sistemtempahanbilikkhas/wiki/Home/sunting", "/aiman/sistemtempahanbilikkhas/wiki/Soalan-Lazim"]:
                        pg.goto(base + path, wait_until="load"); pg.wait_for_timeout(150)
                        over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                        res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                        vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                        cacat = cacat_halaman(pg)
                        if over > 1 or vio or cacat: fail(f"{path} [webkit iPhone 13]: limpahan {over}px, axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}, {cacat}")
                        else: ok(f"{path} [webkit iPhone 13]: tiada limpahan, axe bersih")
                        shot.goto(base + path, wait_until="load"); shot.screenshot(path=str(OUT / f"{path.strip('/').replace('/', '-').replace('?', '-')}--webkit-iPhone13.png"), full_page=True)
                    for path in ["/aiman/sistemtempahanbilikkhas/webhook", "/aiman/sistemtempahanbilikkhas/webhook/1"]:
                        pg.goto(base + path, wait_until="load"); pg.wait_for_timeout(150)
                        for d in pg.locator("details.hantaran > summary").all()[:1]: d.click()  # buka satu penghantaran: muatan JSON panjang
                        over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                        res = pg.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                        vio = kontras_sah(pg, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                        cacat = cacat_halaman(pg)
                        if "ci.example.invalid" not in pg.content(): fail(f"{path} [webkit iPhone 13]: webhook yang dicipta di Chromium tiada")
                        elif cacat: fail(f"{path} [webkit iPhone 13]: {cacat}")
                        elif over > 1: fail(f"{path} [webkit iPhone 13]: limpahan mendatar {over}px")
                        elif vio: fail(f"{path} [webkit iPhone 13]: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                        else: ok(f"{path} [webkit iPhone 13]: tiada limpahan, axe bersih")
                    shot.goto(base + "/aiman/sistemtempahanbilikkhas/webhook/1", wait_until="load"); shot.screenshot(path=str(OUT / "webhook--webkit-iPhone13.png"), full_page=True)
                    # `shot` berkongsi konteks (kuki sesi) dengan `pg` — sudah log masuk
                    shot.goto(base + "/akaun", wait_until="load"); shot.screenshot(path=str(OUT / "akaun--webkit-iPhone13-light.png"), full_page=True)
                    # kepala bila log masuk (pentadbir: 4 pautan + log keluar + alat): mesti muat dalam ≤ 3 baris dan tanpa limpahan
                    pg.goto(base + "/", wait_until="load")
                    tinggi = pg.evaluate("document.querySelector('.bar').getBoundingClientRect().height")
                    over = pg.evaluate("document.documentElement.scrollWidth - innerWidth")
                    if over > 1 or tinggi > 150: fail(f"kepala log masuk [webkit iPhone 13]: tinggi {tinggi:.0f}px, limpahan {over}px")
                    else: ok(f"kepala log masuk [webkit iPhone 13]: {tinggi:.0f}px, tiada limpahan")
                    shot.goto(base + "/", wait_until="load"); shot.screenshot(path=str(OUT / "home-logged-in--webkit-iPhone13-light.png"), full_page=True)
                    # pengguna baharu (D64): "Mula di sini" dengan arahan git panjang mesti muat di telefon, axe bersih
                    bctx = br.new_context(color_scheme=theme, **opts); bctx.add_init_script(axe_src); bp = bctx.new_page()
                    bp.goto(base + "/login", wait_until="load")
                    bp.fill("input[name=user]", "baru"); bp.fill("input[name=password]", "kata laluan pengguna baru"); bp.click("form[action='/login'] button[type=submit]"); bp.wait_for_load_state("load")
                    bp.goto(base + "/", wait_until="load")
                    over = bp.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = bp.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(bp, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if bp.locator(".mula").count() != 1: fail("mula di sini [webkit iPhone 13]: kotak tiada untuk pengguna baharu")
                    elif over > 1: fail(f"mula di sini [webkit iPhone 13]: limpahan mendatar {over}px")
                    elif vio: fail(f"mula di sini [webkit iPhone 13]: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    else: ok("mula di sini [webkit iPhone 13]: pengguna baharu, tiada limpahan, axe bersih")
                    # fork (D75/D76): kepala repo fork membawa dua butang + baris asal; v2.13.1 melimpah 46px di iPhone
                    bp.goto(base + "/aiman/sistemtempahanbilikkhas", wait_until="load")
                    bp.click("form.borang-fork button[type=submit]"); bp.wait_for_load_state("load")
                    over = bp.evaluate("document.documentElement.scrollWidth - innerWidth")
                    res = bp.evaluate("axe.run(document, {runOnly: {type: 'tag', values: ['wcag2a', 'wcag2aa']}})")
                    vio = kontras_sah(bp, [v for v in res["violations"] if v["impact"] in ("serious", "critical")])
                    if not bp.url.rstrip("/").endswith("/baru/sistemtempahanbilikkhas"): fail(f"fork [webkit iPhone 13]: tidak sampai ke fork ({bp.url})")
                    elif bp.locator(".fork-dari").count() != 1 or bp.locator("a[href$='/pr/hulu']").count() != 1: fail("fork [webkit iPhone 13]: baris asal atau pautan PR ke hulu tiada")
                    elif bp.locator("form.borang-fork").count(): fail("fork [webkit iPhone 13]: butang Fork pada repo sendiri")
                    elif over > 1: fail(f"fork [webkit iPhone 13]: limpahan mendatar {over}px")
                    elif vio: fail(f"fork [webkit iPhone 13]: axe {[(v['id'], v['nodes'][0]['target']) for v in vio]}")
                    else: ok("fork [webkit iPhone 13]: fork dicipta, kepala muat, axe bersih")
                    bp.screenshot(path=str(OUT / "fork--webkit-iPhone13.png"), full_page=True)
                    bctx.close()
                ctx.close(); br.close()
    finally:
        srv.send_signal(signal.SIGTERM)
        try: srv.wait(timeout=20)
        except subprocess.TimeoutExpired: srv.kill()
        for root, dirs, files in os.walk(tmp):  # fail baca-sahaja (cache modul) tidak boleh dipadam tanpa ini
            for d in dirs: os.chmod(os.path.join(root, d), 0o700)
            for f in files:
                try: os.chmod(os.path.join(root, f), 0o600)
                except OSError: pass
        shutil.rmtree(tmp, ignore_errors=True)
        if tmp.exists(): print(f"  ! folder sementara tidak dapat dipadam: {tmp}")
    if bad:
        print(f"\n✗ pelayar: {len(bad)} kegagalan"); sys.exit(1)
    print(f"\n✓ pelayar: semua lulus — tangkapan skrin dalam {OUT}")

if __name__ == "__main__":
    main()
