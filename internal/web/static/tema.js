// Codr — pilihan tema pengguna, dibaca SEBELUM CSS dilukis (tiada kelipan). Tiada pilihan = ikut sistem.
(function () { try { var t = localStorage.getItem("codr_tema") || localStorage.getItem("amcd_tema"); if (t === "light" || t === "dark") { document.documentElement.setAttribute("data-tema", t); } } catch (e) {} })();
