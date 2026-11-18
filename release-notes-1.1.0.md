Неофициальный лёгкий Android-клиент Mozilla VPN / Firefox Private Network.
Нативный Kotlin + Jetpack Compose, сетевое ядро на Go.

## Что нового в 1.1.0

- **Домашние сайты.** Сайты выбранной страны (20 стран на выбор) открываются напрямую, с вашего обычного IP: госуслуги, банки и локальные сервисы работают как без VPN. Весь остальной трафик по-прежнему идёт через VPN. Настройка — на вкладке «Консоль».
- **Вход через браузер.** Добавление аккаунта Mozilla теперь идёт через браузерное окно внутри приложения — так же, как на сайте Mozilla, включая регистрацию нового аккаунта и ввод кода из письма. Вход по паролю остался запасным способом.
- **Запоминание местоположения.** Выбранный сервер больше не сбрасывается при перезапуске приложения.
- **Города и честный «Рекомендуемый».** В списке серверов — названия городов вместо кодов аэропортов, а «Рекомендуемый» — настоящий anycast-адрес Mozilla: сеть сама направит на ближайшую точку.
- **Безлимит на время акции.** Пока Mozilla даёт бесплатный трафик без ограничений (до 31 января 2027), карточки аккаунтов показывают «БЕЗЛИМИТНЫЙ ТРАФИК». Когда акция закончится, учёт трафика и автоматическое переключение аккаунтов по лимиту вернутся сами.
- **Здоровье аккаунтов.** Если подключение через аккаунт не удалось, приложение само пробует следующий рабочий. Аккаунты, которым нужен повторный вход, помечаются «НУЖЕН ВХОД» и выпадают из очереди, пока их не починить.
- **Надёжность.** Исправлена гонка при обновлении токенов входа; настройки и список аккаунтов теперь сохраняются атомарно и не повредятся при внезапной остановке приложения.

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

## What's new in 1.1.0

- **Home sites.** Sites of a country you pick (20 countries) open directly, from your regular IP: government services, banks and local sites work as if there were no VPN. All other traffic still goes through the VPN. Configure it on the Console tab.
- **Browser sign-in.** Adding a Mozilla account now happens in a browser window inside the app — the same flow as on Mozilla's site, including registering a new account and entering the email code. Password sign-in remains as a fallback.
- **Location is remembered.** The selected server no longer resets when the app restarts.
- **City names and an honest "Recommended".** The server list shows city names instead of airport codes, and "Recommended" is Mozilla's real anycast endpoint: the network routes you to the nearest point itself.
- **Unlimited during the promo.** While Mozilla offers free traffic without caps (until 31 January 2027), account cards show "UNLIMITED TRAFFIC". When the promo ends, traffic accounting and automatic account rotation by quota resume on their own.
- **Account health.** If connecting through one account fails, the app tries the next working one itself. Accounts that need a fresh sign-in are marked "SIGN-IN REQUIRED" and leave the rotation until fixed.
- **Reliability.** Fixed a race in token refresh; settings and the account list are now saved atomically and survive an abrupt app stop.

## Which file to download

- **Mozilla-VPN-arm64-v8a.apk** — for modern 64-bit phones
  (Google Pixel and almost all Android devices of recent years): smaller.
- **Mozilla-VPN-universal.apk** — for any device: all
  architectures in one file.

The signing key is permanent: new versions install over old ones without uninstalling.
