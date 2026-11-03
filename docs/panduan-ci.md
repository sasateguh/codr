# CI di Codr

Codr menjalankan semakan anda pada setiap tolakan dengan `codr-runner`, program kecil di sebelah forge (D89). Setiap kerja berjalan
dalam kontena tanpa rangkaian, tanpa keistimewaan tambahan dan dengan sistem baca-sahaja, dan hasilnya muncul pada commit dan pada
pull request.

## Tambah kerja

Komit fail bernama `.codr/ci.toml`:

```toml
image = "golang:1.23-alpine"     # imej kontena yang dipakai setiap kerja
branches = ["main", "release/*"] # pilihan: jalan hanya untuk tolakan ke cawangan ini (glob); tiada = setiap cawangan

[jobs.ujian]
run = [
  "test -z \"$(gofmt -l .)\"",
  "go vet ./...",
  "go test ./...",
]
timeout_minutes = 10             # pilihan; pelari ada had atasnya sendiri

[jobs.lint]
image = "koalaman/shellcheck-alpine:stable"   # satu kerja boleh memakai imejnya sendiri
run = ["shellcheck scripts/*.sh"]
```

- Setiap kerja dilaporkan sebagai semakan sendiri, `ci/<nama kerja>`: *pending* semasa menunggu atau berjalan, kemudian *success*
  atau *failure*.
- Langkah berjalan mengikut susunan dengan `sh -e`, jadi langkah pertama yang gagal menamatkan kerja. Log memaparkan setiap langkah
  sebagai `$ langkah`.
- Fail yang silap (kunci tidak dikenali, kerja tanpa langkah, nama imej tidak sah) memberi satu semakan *error* bernama `ci` yang
  menyatakan silapnya. Tiada fail = tiada semakan.
- Dalam kerja, kod berada di `/src`, `HOME` ialah `/tmp`, dan `CI`, `CODR_REPO`, `CODR_BRANCH`, `CODR_SHA` ditetapkan.
- Tiada rangkaian kecuali pentadbir pelayan membenarkannya, jadi kerja tidak boleh memuat turun kebergantungan. Guna imej yang sudah
  ada apa yang kerja perlukan, atau simpan kebergantungan dalam repo.

## Baca hasilnya

Pada halaman commit dan pull request, setiap semakan berpautan ke lognya. Alamat log panjang dan rawak: sesiapa yang ada pautan boleh
membacanya, dan pautan itu hanya dipapar kepada orang yang boleh membaca repo.

## Tuntut semakan hijau

Tetapan repo → Cawangan dilindungi → tambah cawangan dengan **Semakan CI mesti lulus**. Selepas itu cawangan hanya bergerak ke commit
yang semua semakannya lulus: tolakan commit yang belum diuji ditolak (uji di cawangan lain dahulu), dan pull request hanya boleh
digabung bila commit terakhirnya hijau.

## Untuk pentadbir

Pelari dipasang sekali setiap pelayan dan sekali setiap repo (akaun pelari, webhook, rahsianya). Langkahnya dalam `deploy/README.md`
bahagian "Pelari CI".
