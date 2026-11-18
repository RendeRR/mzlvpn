Неофициальный лёгкий Android-клиент Mozilla VPN / Firefox Private Network.
Нативный Kotlin + Jetpack Compose, сетевое ядро на Go.

## Что нового в 1.1.1

- **Тёмная тема «Ночная панель».** Тёмный вариант оформления «Прибора» — глубокий сине-серый фон, бумажные гравировки, тот же сигнальный оранжевый. Режимы «Авто», «Светлая» и «Тёмная» переключаются квадратной кнопкой в углу верхней плашки: полукруг — авто, солнце — светлая, луна — тёмная.
- **Виджет в теме приложения.** Виджет на рабочем столе теперь следует за выбранной темой приложения и перекрашивается сразу при переключении, а не когда сменится системная тема.
- **Локальные адреса — без VPN.** Домашний роутер и устройства локальной сети доступны при включённом VPN: адреса локальных сетей (192.168.x.x, 10.x.x.x и другие приватные диапазоны) больше не уходят в туннель и открываются напрямую, как без VPN. Весь остальной трафик по-прежнему идёт через VPN.

## Какой файл качать

- **Mozilla-VPN-arm64-v8a.apk** — для современных 64-битных телефонов
  (Google Pixel и почти все Android последних лет): меньше размер.
- **Mozilla-VPN-universal.apk** — для любого устройства: все
  архитектуры в одном файле.

Подпись постоянная: новые версии устанавливаются поверх старых без удаления.

---

Unofficial lightweight Android client for Mozilla VPN / Firefox Private
Network. Native Kotlin + Jetpack Compose with a Go networking core.
Requires a Mozilla account with VPN/FPN access; not affiliated with Mozilla.

## What's new in 1.1.1

- **Dark theme "Night Panel".** A dark take on the instrument-panel design — deep blue-grey background, paper engravings, the same signal orange. Auto, Light and Dark modes switch from the square button in the corner of the top plate: a half-disc for auto, a sun for light, a moon for dark.
- **The widget follows the app theme.** The home-screen widget now matches the theme selected in the app and repaints the moment you switch it, instead of waiting for the system theme to change.
- **Local addresses skip the VPN.** Your home router and LAN devices stay reachable with the VPN on: local-network addresses (192.168.x.x, 10.x.x.x and other private ranges) no longer enter the tunnel and open directly, as if there were no VPN. All other traffic still goes through the VPN.

## Which file to download

- **Mozilla-VPN-arm64-v8a.apk** — for modern 64-bit phones
  (Google Pixel and almost all Android devices of recent years): smaller.
- **Mozilla-VPN-universal.apk** — for any device: all
  architectures in one file.

The signing key is permanent: new versions install over old ones without uninstalling.
