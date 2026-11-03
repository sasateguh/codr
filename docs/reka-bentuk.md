# Bagaimana Codr dibina

Codr ialah forge git yang boleh dijalankan dan difahami oleh seorang: satu binari, satu fail pangkalan data, satu folder
repo git. Halaman ini menerangkan pilihan di sebaliknya dan peraturan yang dipegang kod. Setiap pilihan ada rekod keputusan
bernombor (D1, D2, …) yang disimpan penyelenggara, bersama apa yang digantinya dan cara mengundurnya; nombor dalam
komen kod merujuk kepadanya.

## Satu binari, git yang mengurus git

Forge ini satu program Go, `codr`. Ia melayan halaman web, pintu git HTTPS dan SSH, API dan hook. Ia tidak menghurai format
pack atau protokol git sendiri. Klon dan tolakan menjalankan binari `git` sebenar (`git upload-pack`, `git receive-pack`,
`git http-backend`), jadi setiap klien git terus berfungsi dan pembaikan keselamatan git terpakai pada hari ia keluar (D2).

Perubahan yang web buat pada repo (gabung, gabung squash dan rebase, halaman wiki) memakai plumbing git pada repo itu:
`merge-tree` membina hasilnya dalam ingatan, `commit-tree` menulis commit, dan `update-ref` menggerakkan cawangan hanya jika
ia masih di tempat halaman membacanya. Tiada working tree, tiada fail kunci untuk dibersihkan, dan cawangan yang bergerak di
tengah jalan ditolak, bukan ditindih (D53, D83, D88).

## Satu fail pangkalan data

Semua yang bukan git hidup dalam satu fail SQLite mod WAL: akaun, kebenaran, isu, pull request, pemberitahuan, baris audit,
tetapan (D3). Migrasi hanya menambah. Repo di cakera dinamakan ikut id pangkalan data (`repos/<id>.git`), bukan nama yang
ditaip orang, jadi nama semula atau nama berniat jahat tidak sampai ke sistem fail (D27).

Backup ialah snapshot yang konsisten: pangkalan data melalui `VACUUM INTO` SQLite, setiap repo sebagai bundle git yang disahkan,
fail besar (LFS) dan repo wiki bersama. Setiap malam backup itu dipulihkan ke folder kosong dan disemak, kerana backup yang
tidak pernah dipulihkan hanyalah harapan (D41).

## Satu keputusan kebenaran

Siapa boleh membaca atau menulis sesuatu repo dijawab oleh satu fungsi, `AccessFor`, dan setiap pintu bertanya kepadanya
sebelum git dijalankan: SSH, HTTPS, halaman web dan API. Repo persendirian yang tidak boleh dibaca menjawab "tiada", bukan
"dilarang", supaya kewujudannya tidak bocor. Organisasi dan pasukan masuk ke fungsi yang sama (D74). Ujian membandingkannya
dengan pertanyaan senarai yang menentukan apa yang dilihat seseorang, jadi kedua-duanya tidak boleh menyimpang.

Peraturan yang sama mengikut setiap perkara yang memaparkan kandungan kepada orang lain: pemberitahuan hanya ditulis untuk
orang yang boleh membaca repo itu, aktiviti profil ditapis ikut akses pelawat, dan rujukan `owner/repo#12` menjadi pautan
hanya untuk pembaca repo itu (D68, D79, D87).

## Peraturan tolakan di satu tempat

Cawangan dilindungi, had saiz dan semakan wajib dikuatkuasakan oleh hook git yang memanggil semula pelayan yang sedang
berjalan melalui soket tempatan, jadi pintu HTTPS dan SSH berkongsi satu pelaksanaan (D31). Cawangan dilindungi boleh
menolak tolakan paksa, pemadaman, dan bila diminta, sebarang commit yang semakan CI-nya belum semua lulus (D89).

## Peristiwa, ditulis sekali

Setiap perubahan yang orang ambil berat (tolakan, isu dibuka, semakan, gabung) ditulis sekali ke jadual peti keluar, di
sebelah baris auditnya. Pembaca yang berasingan mengikut peti keluar itu dengan kursor masing-masing: penghantaran webhook
(ditandatangani HMAC, dicuba semula, disemak terhadap alamat persendirian selepas DNS), pemberitahuan dan emel, dan pembaca
yang menutup isu yang dinamakan `fixes #12` sebaik commit sampai ke cawangan lalai. Pembaca yang lambat tidak pernah
melambatkan tolakan (D78, D79, D87).

## CI di luar forge

`codr-runner` ialah program kecil yang berasingan. Ia menerima webhook tolakan, menyemak tandatangannya, mengambil commit
itu dan menjalankan kerja dalam `.codr/ci.toml`, setiap satu dalam kontena tanpa rangkaian, tanpa keupayaan, dengan sistem
fail akar baca-sahaja dan had memori, CPU serta proses. Keputusan kembali sebagai status commit melalui API, dengan pautan
ke log kerja (D89). Forge sendiri tidak menjalankan kod pengguna.

## Rahsia

Kata laluan di-hash dengan Argon2id. Token sesi dan akses disimpan sebagai hash; token dipapar sekali. Rahsia yang pelayan
perlukan kemudian (token cermin, rahsia webhook, kata laluan mel, kunci backup) disegel dengan kunci yang hidup dalam fail
konfigurasi, bukan dalam pangkalan data, jadi salinan pangkalan data sahaja tidak membuka apa-apa (D28, D38, D70). Log masuk
boleh menuntut kod TOTP; langkah antara kata laluan dan kod ialah cabaran, bukan sesi (D80).

## Teks dan tetapan

Tiada ayat untuk pengguna dalam kod. Setiap ayat hidup dalam `locale/ms-MY.json` dan `locale/en-US.json`, dan gerbang binaan
gagal bila templat atau mesej ralat membawa teks tertanam (D34). Nilai yang munasabah berubah (saiz halaman, had, tamat masa,
senarai kata kunci) ialah tetapan dalam pangkalan data, boleh diubah tanpa deploy. Bahasa Melayu dan Inggeris setara dari
halaman pertama.

## Gerbang

Tiada apa ditag sehingga gerbang penuh lulus pada pokok kod yang tepat itu: format, `go vet`, `staticcheck`, `gosec`,
`govulncheck`, ujian dengan pengesan perlumbaan, had saiz setiap fail, imbasan teks tertanam, dan pelayar sebenar (Chromium,
dan WebKit sebagai iPhone 13) dalam kedua-dua tema dengan semakan kebolehcapaian pada setiap halaman. Skrip deploy menolak tag
yang pokoknya tiada rekod larian penuh hijau, menyimpan versi sebelumnya, dan berundur sendiri bila versi baharu tidak
menjawab dengan versinya (D42, D47).

## Apa yang ia tidak cuba jadi

Codr bukan perkhidmatan hosting untuk banyak penyewa, tidak cuba menandingi GitHub Actions ciri demi ciri, dan tidak
menjalankan pelayan mel masuk. Had ini ditulis supaya ia kekal had sehingga ada sebab untuk mengubahnya.
