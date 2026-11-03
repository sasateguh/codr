// Codr — laman pendaratan (D56, D58). Dua tingkah laku, kedua-duanya progresif:
// 1) Graf commit 3D: dari depan ia huruf C pada tanda Codr (satu sejarah yang melengkung); cawangan ciri keluar ke
//    kedalaman (paksi z) dan bergabung semula. Tatal ke bawah = ia berpusing ke sisi dan sejarahnya kelihatan, dengan
//    label commit SEBENAR dari repo contoh (data-commits). Kanvas 2D + unjuran perspektif sendiri, tiada pustaka;
//    warna dari token CSS (ikut tema); berhenti di luar skrin; prefers-reduced-motion = bingkai statik. Tanpa JS = SVG.
//    Setiap denyut biru yang bergerak ialah satu tolakan yang tiba; riak di hujung atas = commit baharu (D60).
// 2) Terminal: baris muncul satu demi satu bila kelihatan; gerakan dikurangkan / tanpa JS = semua terus kelihatan.
// 3) Bahagian .p-muncul muncul sekali bila masuk skrin; tangkapan skrin .p-condong menegak semasa ditatal. Kedua-duanya
//    hanya bila html.p-gerak (ditambah di sini), jadi tanpa JS atau dengan gerakan dikurangkan semuanya statik.
(function () {
  "use strict";
  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ── Muncul + condong ──
  if (!still && window.IntersectionObserver) {
    document.documentElement.classList.add("p-gerak");
    var seen = new IntersectionObserver(function (en) {
      en.forEach(function (e) {
        if (!e.isIntersecting) { return; }
        var el = e.target, sib = el.parentNode ? Array.prototype.indexOf.call(el.parentNode.children, el) : 0;
        el.style.transitionDelay = Math.min(sib, 5) * 70 + "ms";
        el.classList.add("nampak"); seen.unobserve(el);
      });
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.12 });
    Array.prototype.forEach.call(document.querySelectorAll(".p-muncul"), function (el) { seen.observe(el); });
    var tilts = document.querySelectorAll(".p-condong"), tiltRaf = 0;
    var tilt = function () {
      tiltRaf = 0;
      Array.prototype.forEach.call(tilts, function (el) {
        var r = el.getBoundingClientRect(), vh = window.innerHeight || 800;
        var t = Math.min(1, Math.max(0, (vh - r.top) / (vh * 0.75)));
        el.style.setProperty("--condong", (1 - t * (2 - t)).toFixed(3));
      });
    };
    if (tilts.length) {
      window.addEventListener("scroll", function () { if (!tiltRaf) { tiltRaf = requestAnimationFrame(tilt); } }, { passive: true });
      tilt();
    }
  }

  // ── Terminal ──
  var term = document.querySelector(".p-terminal-hidup");
  if (term && !still && window.IntersectionObserver) {
    var lines = term.querySelectorAll("code > span");
    term.classList.add("siap");
    var io = new IntersectionObserver(function (en) {
      if (!en[0].isIntersecting) { return; }
      io.disconnect();
      Array.prototype.forEach.call(lines, function (l, i) {
        var arahan = l.classList.contains("p-arahan");
        setTimeout(function () { l.classList.add("papar"); }, 250 + i * 260 + (arahan && i > 0 ? 500 : 0));
      });
    }, { threshold: 0.4 });
    io.observe(term);
  }

  // ── Graf 3D ──
  var canvas = document.getElementById("graf3d");
  if (!canvas || !canvas.getContext) { return; }
  var ctx = canvas.getContext("2d");
  if (!ctx) { return; }
  var stage = canvas.parentNode, hero = document.querySelector(".p-hero");
  var commits = [];
  try { commits = JSON.parse(canvas.getAttribute("data-commits") || "[]") || []; } catch (e) { commits = []; }

  // Geometri: lengkung C dalam satah z = 0, jejari 1, dari 48° (hujung atas) melalui 180° ke 312° (hujung bawah).
  // Sejarah mengalir dari hujung bawah (lama) ke hujung atas (terbaru), sama seperti tanda.
  var A0 = 48 * Math.PI / 180, A1 = 312 * Math.PI / 180, R = 1;
  function onArc(t) { var a = A1 - (A1 - A0) * t; return [R * Math.cos(a), R * Math.sin(a), 0]; } // t 0 = bawah, 1 = atas
  function lerp(a, b, t) { return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t]; }
  var nodes = [], edges = [];
  function node(p, kind, order, label) { nodes.push({ p: p, kind: kind, order: order, label: label }); return nodes.length - 1; }
  function edge(a, b, kind, order) { edges.push({ a: a, b: b, kind: kind, order: order }); }

  var N = 12, main = [];
  for (var i = 0; i <= N; i++) {
    var t = i / N, end = i === 0 || i === N || i === N / 2;
    main.push(node(onArc(t), end ? "ujung" : "commit", t * 0.85));
  }
  // smooth main line: sample the arc between commits
  for (i = 0; i < N; i++) {
    var prev = main[i];
    for (var k = 1; k <= 4; k++) {
      var id = k === 4 ? main[i + 1] : node(onArc((i + k / 4) / N), "titik", (i + k / 4) / N * 0.85);
      edge(prev, id, "utama", (i + k / 4) / N * 0.85); prev = id;
    }
  }
  // labels: newest commit at the top end, walking down the arc
  for (i = 0; i < commits.length && i < 6; i++) { nodes[main[N - i * 2]].label = commits[i]; }

  // feature branches: leave the line, travel into depth along the arc, merge back further on
  var specs = [];
  function onBranch(b, u) { var q = onArc((b.f + (b.t - b.f) * u) / N); q[2] = b.amp * Math.sin(Math.PI * u); return q; }
  function branch(fromI, toI, amp, count, order) {
    specs.push({ f: fromI, t: toI, amp: amp });
    var SEG = 20, prevId = main[fromI], every = Math.round(SEG / (count + 1));
    for (var s = 1; s < SEG; s++) {
      var q = onArc((fromI + (toI - fromI) * s / SEG) / N);
      q[2] = amp * Math.sin(Math.PI * s / SEG);
      var isCommit = s % every === 0 && s / every <= count;
      var nid = node(q, isCommit ? "cabang" : "titik", order + 0.15 * s / SEG);
      edge(prevId, nid, "cabang", order + 0.15 * s / SEG); prevId = nid;
    }
    edge(prevId, main[toI], "cabang", order + 0.15);
  }
  branch(1, 4, 0.8, 3, 0.1);
  branch(5, 8, -0.75, 2, 0.4);
  branch(8, 11, 0.6, 2, 0.62);

  var FLOOR_Y = -1.35, floor = [];
  for (var z = -1.6; z <= 1.61; z += 0.26) { floor.push({ a: [-1.6, FLOOR_Y, z], b: [1.6, FLOOR_Y, z], kind: "garis" }); }
  floor.push({ a: [-1.22, FLOOR_Y, -1.6], b: [-1.22, FLOOR_Y, 1.6], kind: "margin" });

  var ink, soft, paper, accent, margin, mono;
  // depth is shown by mixing toward the paper colour, not by transparency: overlapping round caps on a
  // translucent line show as beads, a solid mixed colour does not
  function rgb(c) {
    var m = /^#([0-9a-f]{6})$/i.exec(c);
    if (m) { var n = parseInt(m[1], 16); return [n >> 16, (n >> 8) & 255, n & 255]; }
    m = c.match(/[\d.]+/g); return m ? [+m[0], +m[1], +m[2]] : [0, 0, 0];
  }
  function mix(fg, t) { var a = rgb(fg), b = rgb(paper); return "rgb(" + [0, 1, 2].map(function (i) { return Math.round(b[i] + (a[i] - b[i]) * t); }).join(",") + ")"; }
  function colours() {
    var cs = getComputedStyle(document.documentElement);
    var v = function (n, d) { var x = cs.getPropertyValue(n).trim(); return x || d; };
    ink = v("--dakwat", "#15171c"); soft = v("--dakwat-lembut", "#5f6571"); paper = v("--kertas", "#f6f6f3");
    accent = v("--pen-biru", "#1b4fd8"); margin = v("--merah-lejar", "#b42318"); mono = v("--mono", "monospace");
  }

  var W = 0, H = 0, S = 1, CX = 0, CY = 0, D = 4.6, PITCH = 0.16;
  var yaw = still ? -0.85 : -0.12, dragYaw = 0, dragPitch = 0, scrollT = 0;
  function project(p, cy, sy, cp, sp) {
    var x = p[0] * cy + p[2] * sy, z = -p[0] * sy + p[2] * cy, y = p[1];
    var y2 = y * cp - z * sp, z2 = y * sp + z * cp, k = D / (D - z2);
    return { x: CX + x * k * S, y: CY - y2 * k * S, z: z2, k: k };
  }
  function depthAlpha(z) { return 0.45 + 0.55 * Math.min(1, Math.max(0, (z + 0.9) / 1.6)); }
  function resize() {
    var r = stage.getBoundingClientRect(), dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = r.width; H = r.height;
    canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    S = Math.min(W, H) * 0.3; CX = W * 0.47; CY = H * 0.46;
  }

  function draw(progress) {
    ctx.clearRect(0, 0, W, H);
    var a = yaw + dragYaw, pt = PITCH + dragPitch + 0.12 * scrollT;
    CX = W * (0.47 - 0.17 * scrollT); // the graph steps left as it turns, making room for its labels
    var cy = Math.cos(a), sy = Math.sin(a), cp = Math.cos(pt), sp = Math.sin(pt);
    var P = nodes.map(function (n) { return project(n.p, cy, sy, cp, sp); });
    var items = [];
    floor.forEach(function (f) {
      for (var s = 0; s < 12; s++) {
        items.push({ z: -9, t: f.kind, p1: project(lerp(f.a, f.b, s / 12), cy, sy, cp, sp), p2: project(lerp(f.a, f.b, (s + 1) / 12), cy, sy, cp, sp), f: Math.sin(Math.PI * (s + 0.5) / 12) });
      }
    });
    edges.forEach(function (e) { if (e.order <= progress) { items.push({ z: (P[e.a].z + P[e.b].z) / 2 - 0.001, t: e.kind, p1: P[e.a], p2: P[e.b] }); } });
    pulses.forEach(function (q) {
      var u = Math.min(1, (clock - q.t0) / q.dur), e = u < 0.5 ? 2 * u * u : 1 - Math.pow(-2 * u + 2, 2) / 2;
      if (u >= 1) { return; }
      for (var j = 0; j < 4; j++) {
        var uu = Math.max(0, e - j * 0.018), pt3 = q.b ? onBranch(q.b, uu) : onArc(uu);
        var pp = project(pt3, cy, sy, cp, sp);
        items.push({ z: pp.z + 0.002, t: "denyut", p: pp, j: j });
      }
    });
    ripples.forEach(function (q) {
      var u = (clock - q.t0) / 900;
      if (u < 0 || u >= 1) { return; }
      var pp = project(onArc(1), cy, sy, cp, sp);
      items.push({ z: pp.z + 0.003, t: "riak", p: pp, u: u });
    });
    nodes.forEach(function (n, i) { if (n.kind !== "titik" && n.order <= progress) { items.push({ z: P[i].z, t: n.kind, p: P[i], g: Math.min(1, (progress - n.order) * 8), n: n }); } });
    items.sort(function (x, y) { return x.z - y.z; });
    items.forEach(function (it) {
      if (it.p1) {
        var zz = (it.p1.z + it.p2.z) / 2, k = (it.p1.k + it.p2.k) / 2;
        if (it.t === "garis" || it.t === "margin") {
          ctx.globalAlpha = (it.t === "margin" ? 0.4 : 0.32) * it.f * Math.min(1, Math.max(0.15, (zz + 1.6) / 2.6));
          ctx.strokeStyle = it.t === "margin" ? margin : soft; ctx.lineWidth = 1; ctx.lineCap = "butt";
        } else {
          ctx.globalAlpha = 1; ctx.strokeStyle = mix(it.t === "cabang" ? accent : ink, depthAlpha(zz));
          ctx.lineWidth = (it.t === "cabang" ? 0.022 : 0.042) * S * k; ctx.lineCap = it.t === "cabang" ? "butt" : "round";
        }
        ctx.beginPath(); ctx.moveTo(it.p1.x, it.p1.y); ctx.lineTo(it.p2.x, it.p2.y); ctx.stroke();
        return;
      }
      var p = it.p, r, dA = depthAlpha(p.z);
      ctx.globalAlpha = 1;
      if (it.t === "denyut") {
        r = (0.05 - it.j * 0.009) * S * p.k;
        if (it.j === 0) { ctx.fillStyle = paper; ctx.beginPath(); ctx.arc(p.x, p.y, r + 0.022 * S * p.k, 0, 6.2832); ctx.fill(); }
        ctx.fillStyle = mix(accent, dA * (1 - it.j * 0.22)); ctx.beginPath(); ctx.arc(p.x, p.y, r, 0, 6.2832); ctx.fill();
        return;
      }
      if (it.t === "riak") {
        ctx.globalAlpha = (1 - it.u) * 0.9; ctx.strokeStyle = accent; ctx.lineWidth = Math.max(1, 0.012 * S);
        ctx.beginPath(); ctx.arc(p.x, p.y, (0.13 + 0.3 * it.u) * S * p.k, 0, 6.2832); ctx.stroke(); ctx.globalAlpha = 1;
        return;
      }
      if (it.t === "ujung") {
        r = 0.12 * S * p.k * it.g;
        ctx.fillStyle = mix(ink, dA); ctx.beginPath(); ctx.arc(p.x, p.y, r, 0, 6.2832); ctx.fill();
        ctx.fillStyle = paper; ctx.beginPath(); ctx.arc(p.x, p.y, r * 0.42, 0, 6.2832); ctx.fill();
      } else {
        r = (it.t === "cabang" ? 0.05 : 0.058) * S * p.k * it.g;
        ctx.fillStyle = paper; ctx.beginPath(); ctx.arc(p.x, p.y, r + 0.02 * S * p.k, 0, 6.2832); ctx.fill();
        ctx.fillStyle = mix(it.t === "cabang" ? accent : ink, dA); ctx.beginPath(); ctx.arc(p.x, p.y, r, 0, 6.2832); ctx.fill();
      }
      // commit label: short hash + subject, appears as the graph turns to its side
      if (it.n.label && W > 520) {
        var show = Math.min(1, Math.max(0, (Math.abs(a) - 0.35) / 0.4)) * Math.min(1, it.g);
        if (show > 0.02) {
          ctx.globalAlpha = show * depthAlpha(p.z);
          ctx.font = "500 " + Math.round(11 + 2 * p.k) + "px " + mono;
          ctx.textBaseline = "middle";
          var x0 = p.x + 0.17 * S * p.k;
          var room = W - 8 - x0, hw = ctx.measureText(it.n.label.h + "  ").width, subj = it.n.label.s;
          if (room > hw + 40) {
            while (subj.length > 4 && hw + ctx.measureText(subj).width > room) { subj = subj.slice(0, -2).replace(/\s+$/, "") + "…"; subj = subj.replace(/……$/, "…"); }
            ctx.fillStyle = soft; ctx.fillText(it.n.label.h, x0, p.y);
            ctx.fillStyle = ink; ctx.fillText(subj, x0 + hw, p.y);
          }
        }
      }
    });
    ctx.globalAlpha = 1;
  }

  var pulses = [], ripples = [], nextPulse = 0, pulseN = 0;
  // a push every few seconds: most travel the main line to the newest end, every third one runs a branch
  function tickPulses() {
    if (still || clock < INTRO + 300) { return; }
    if (clock >= nextPulse) {
      var b = pulseN % 3 === 2 && specs.length ? specs[(pulseN / 3 | 0) % specs.length] : null;
      pulses.push({ t0: clock, dur: b ? 1500 : 2600, b: b }); pulseN++;
      nextPulse = clock + 1900 + (pulseN % 2) * 900;
    }
    pulses = pulses.filter(function (q) {
      if (clock - q.t0 < q.dur) { return true; }
      if (!q.b) { ripples.push({ t0: clock }); }
      return false;
    });
    ripples = ripples.filter(function (q) { return clock - q.t0 < 900; });
  }
  var clock = 0, last = 0, running = false, visible = true, raf = 0, dragging = false, lastX = 0, lastY = 0;
  var INTRO = 1700, PERIOD = 18000;
  function heroT() {
    if (!hero) { return 0; }
    var r = hero.getBoundingClientRect(), span = r.height - window.innerHeight; // sticky hero: the extra height is the turn
    if (span < 200) { span = r.height * 0.75; }
    return Math.min(1, Math.max(0, -r.top / span));
  }
  function frame(ts) {
    raf = 0;
    var dt = last ? Math.min(64, ts - last) : 16; last = ts; clock += dt;
    var progress = Math.min(1, clock / INTRO); progress = 1 - Math.pow(1 - progress, 3);
    var target = -0.12 - 1.0 * heroT();
    scrollT += (heroT() - scrollT) * Math.min(1, dt / 160);
    var sway = 0.14 * Math.sin(2 * Math.PI * clock / PERIOD) * (1 - scrollT);
    yaw += (target + sway - yaw) * Math.min(1, dt / 220);
    if (!dragging) { var decay = Math.exp(-dt / 1400); dragYaw *= decay; dragPitch *= decay; }
    tickPulses();
    draw(progress * 1.0001);
    if (running) { raf = requestAnimationFrame(frame); }
  }
  function play() { if (still || running || !visible || document.hidden) { return; } running = true; last = 0; raf = requestAnimationFrame(frame); }
  function pause() { running = false; if (raf) { cancelAnimationFrame(raf); raf = 0; } }
  function redrawStill() { if (still || !running) { draw(1.0001); } }

  stage.addEventListener("pointerdown", function (e) { dragging = true; lastX = e.clientX; lastY = e.clientY; try { canvas.setPointerCapture(e.pointerId); } catch (x) {} });
  stage.addEventListener("pointermove", function (e) {
    if (!dragging) { return; }
    dragYaw += (e.clientX - lastX) / Math.max(1, W) * Math.PI;
    dragPitch = Math.max(-0.35, Math.min(0.5, dragPitch + (e.clientY - lastY) / Math.max(1, H) * 1.2));
    lastX = e.clientX; lastY = e.clientY; redrawStill();
  });
  function release() { dragging = false; }
  stage.addEventListener("pointerup", release);
  stage.addEventListener("pointercancel", release);

  colours(); resize();
  canvas.hidden = false;
  var svg = stage.querySelector(".p-statik");
  if (svg) { svg.setAttribute("hidden", ""); }
  var hint = document.querySelector(".p-petunjuk");
  if (hint) { hint.hidden = false; }
  draw(still ? 1.0001 : 0);

  if (window.ResizeObserver) { new ResizeObserver(function () { resize(); redrawStill(); }).observe(stage); }
  else { window.addEventListener("resize", function () { resize(); redrawStill(); }); }
  var recolour = function () { colours(); redrawStill(); };
  new MutationObserver(recolour).observe(document.documentElement, { attributes: true, attributeFilter: ["data-tema"] });
  if (window.matchMedia) { var mq = window.matchMedia("(prefers-color-scheme: dark)"); if (mq.addEventListener) { mq.addEventListener("change", recolour); } }
  if (window.IntersectionObserver) { new IntersectionObserver(function (en) { visible = en[0].isIntersecting; if (visible) { play(); } else { pause(); } }).observe(stage); }
  document.addEventListener("visibilitychange", function () { if (document.hidden) { pause(); } else { play(); } });
  play();
})();
