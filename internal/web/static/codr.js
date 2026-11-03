// Codr — dua tingkah laku progresif: butang "Salin" dan togol tema. Tanpa JS: kotak teks boleh dipilih, tema ikut sistem.
(function () {
  "use strict";
  var buttons = document.querySelectorAll("button.salin[data-salin]");
  if (navigator.clipboard) {
    Array.prototype.forEach.call(buttons, function (b) {
      b.hidden = false;
      b.addEventListener("click", function () {
        var src = document.getElementById(b.getAttribute("data-salin"));
        var text = src ? (src.value !== undefined ? src.value : src.textContent) : "";
        navigator.clipboard.writeText(text).then(function () {
          var asal = b.textContent;
          b.textContent = b.getAttribute("data-disalin-teks") || asal;
          b.setAttribute("data-disalin", "1");
          setTimeout(function () { b.textContent = asal; b.removeAttribute("data-disalin"); }, 2000);
        });
      });
    });
  }
  var tema = document.querySelector("button.tema");
  if (tema) {
    var root = document.documentElement;
    var semasa = function () {
      var t = root.getAttribute("data-tema");
      if (t === "light" || t === "dark") { return t; }
      return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    };
    var label = function () { tema.setAttribute("aria-label", tema.getAttribute(semasa() === "dark" ? "data-teks-cerah" : "data-teks-gelap")); };
    tema.hidden = false;
    label();
    tema.addEventListener("click", function () {
      var baharu = semasa() === "dark" ? "light" : "dark";
      root.setAttribute("data-tema", baharu);
      try { localStorage.setItem("codr_tema", baharu); } catch (e) {}
      label();
    });
  }
})();

// Menu pengguna (D61): tutup bila klik di luar atau Escape. "/" atau Ctrl/Cmd-K = fokus carian repo.
(function () {
  "use strict";
  var menus = document.querySelectorAll("details.menu-pengguna");
  document.addEventListener("click", function (e) {
    Array.prototype.forEach.call(menus, function (m) { if (m.open && !m.contains(e.target)) { m.open = false; } });
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      Array.prototype.forEach.call(menus, function (m) { if (m.open) { m.open = false; m.querySelector("summary").focus(); } });
      return;
    }
    var t = e.target, typing = t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
    var cari = document.getElementById("cari");
    if (!cari) { return; }
    if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey) && window.codrPalet) {
      e.preventDefault(); window.codrPalet(); return;
    }
    if ((e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) || (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey))) {
      e.preventDefault(); cari.focus(); cari.select();
    }
  });
})();

// Palet arahan (D68): Ctrl/Cmd-K. Halaman dari <template id="palet"> (teks locale), repo dari /api/cari (kebenaran yang sama
// dengan senarai repo). Dibina dengan DOM sahaja: tiada HTML sebaris, tiada gaya sebaris, CSP kekal. Tanpa JS: borang carian biasa.
(function () {
  "use strict";
  var tpl = document.getElementById("palet");
  if (!tpl || !window.HTMLDialogElement || !window.fetch) { return; }
  var t = function (k) { return tpl.getAttribute("data-" + k) || ""; };
  var pages = Array.prototype.map.call(tpl.content.querySelectorAll("a"), function (a) { return { text: a.textContent.trim(), href: a.getAttribute("href") }; });
  var dlg = null, input, list, items = [], active = 0, timer = 0, seq = 0;
  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) { e.className = cls; } if (text) { e.textContent = text; } return e; }
  function build() {
    dlg = el("dialog", "palet");
    dlg.setAttribute("aria-label", t("tajuk"));
    input = el("input", "palet-cari");
    input.type = "search"; input.placeholder = t("cari"); input.autocomplete = "off";
    input.setAttribute("role", "combobox"); input.setAttribute("aria-expanded", "true"); input.setAttribute("aria-controls", "palet-senarai");
    input.setAttribute("aria-label", t("tajuk"));
    list = el("ul", "palet-senarai"); list.id = "palet-senarai"; list.setAttribute("role", "listbox");
    dlg.appendChild(input); dlg.appendChild(list); dlg.appendChild(el("p", "palet-kaki", t("petunjuk")));
    document.body.appendChild(dlg);
    input.addEventListener("input", function () { clearTimeout(timer); timer = setTimeout(refresh, 120); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); move(e.key === "ArrowDown" ? 1 : -1); }
      else if (e.key === "Enter") { e.preventDefault(); if (items[active]) { location.href = items[active].href; } }
    });
    dlg.addEventListener("click", function (e) { if (e.target === dlg) { dlg.close(); } });
  }
  function move(d) { if (!items.length) { return; } active = (active + d + items.length) % items.length; paint(); }
  function paint() {
    Array.prototype.forEach.call(list.querySelectorAll("[role=option]"), function (o, i) { o.setAttribute("aria-selected", i === active ? "true" : "false"); if (i === active) { input.setAttribute("aria-activedescendant", o.id); o.scrollIntoView({ block: "nearest" }); } });
  }
  function render(repos, q) {
    list.textContent = ""; items = [];
    var ql = q.toLowerCase();
    var group = function (title, rows) {
      if (!rows.length) { return; }
      list.appendChild(el("li", "palet-kumpulan", title)).setAttribute("role", "presentation");
      rows.forEach(function (r) {
        var li = el("li", "palet-pilihan"); li.setAttribute("role", "option"); li.id = "palet-" + items.length;
        var a = el("a"); a.href = r.href; a.tabIndex = -1; a.appendChild(el("span", "palet-teks", r.text));
        if (r.badge) { a.appendChild(el("span", "lencana", r.badge)); }
        li.appendChild(a); list.appendChild(li); items.push(r);
        var i = items.length - 1;
        li.addEventListener("mousemove", function () { if (active !== i) { active = i; paint(); } });
      });
    };
    group(t("repo"), repos.map(function (r) { return { text: r.p, href: "/" + r.p, badge: r.k ? t("peribadi") : (r.m ? t("cermin") : "") }; }));
    group(t("tindakan"), pages.filter(function (p) { return !ql || p.text.toLowerCase().indexOf(ql) >= 0; }));
    if (q) { group("", [{ text: t("semua").replace("%s", q), href: "/?q=" + encodeURIComponent(q) }]); }
    if (!items.length) { list.appendChild(el("li", "palet-kosong", t("kosong"))); }
    active = 0; paint();
  }
  function refresh() {
    var q = input.value.trim(), mine = ++seq;
    fetch("/api/cari?q=" + encodeURIComponent(q), { credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : []; })
      .catch(function () { return []; })
      .then(function (repos) { if (mine === seq) { render(repos || [], q); } });
  }
  window.codrPalet = function () {
    if (!dlg) { build(); }
    if (dlg.open) { dlg.close(); return; }
    input.value = ""; dlg.showModal(); input.focus(); refresh();
  };
})();


// repository tabs scroll sideways on a phone; bring the current one into view so it is never cut at the edge
(function () {
  var a = document.querySelector("nav.tab [aria-current=page]");
  if (!a) return;
  var nav = a.parentNode, r = a.getBoundingClientRect(), n = nav.getBoundingClientRect();
  if (r.right > n.right) nav.scrollLeft += r.right - n.right + 16;
})();

// a faded edge says there are more tabs to scroll to, on whichever side still has some
(function () {
  Array.prototype.forEach.call(document.querySelectorAll("nav.tab"), function (nav) {
    var edge = function () {
      nav.classList.toggle("tab-kiri", nav.scrollLeft > 2);
      nav.classList.toggle("tab-kanan", nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2);
    };
    edge();
    nav.addEventListener("scroll", edge, { passive: true });
    window.addEventListener("resize", edge);
  });
})();
