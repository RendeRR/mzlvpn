# Mozilla VPN Lite (mzlvpn)

Лёгкий неофициальный Android-клиент Mozilla VPN / Firefox Private Network.
Нативный Kotlin + Jetpack Compose, сетевое ядро на Go. Дизайн «Прибор»:
бумага, чернила и один сигнальный оранжевый — без градиентов и круглых кнопок.

## Скриншоты

<table>
  <tr>
    <td align="center"><img src="docs/screenshots/%D0%93%D0%BB%D0%B0%D0%B2%D0%BD%D1%8B%D0%B9%20%D1%8D%D0%BA%D1%80%D0%B0%D0%BD.png" width="260" height="584" alt="Главный экран: подключено, рубильник в положении ВКЛ"></td>
    <td align="center"><img src="docs/screenshots/%D0%90%D0%BA%D0%BA%D0%B0%D1%83%D0%BD%D1%82%D1%8B.png" width="260" height="584" alt="Аккаунты: квоты, ротация, кнопка «В работу»"></td>
    <td align="center"><img src="docs/screenshots/%D0%9B%D0%BE%D0%BA%D0%B0%D1%86%D0%B8%D0%B8.png" width="260" height="584" alt="Локации: список стран по пингу"></td>
  </tr>
  <tr>
    <td align="center">Пульт</td>
    <td align="center">Аккаунты</td>
    <td align="center">Локации</td>
  </tr>
</table>

## Возможности

- Вход в аккаунт Mozilla (FxA): пароль, код подтверждения из письма,
  двухфакторная проверка — прямо в приложении.
- Несколько аккаунтов с автоматической ротацией по месячной квоте
  (порог настраивается, по умолчанию 95%) и предпрогревом следующего.
- 32 локации, выбор «самой быстрой» по реальному пингу с устройства.
- Два транспорта: **HTTP/2 CONNECT** (по умолчанию) и HTTP/3 MASQUE.
  HTTP/3 (QUIC/UDP) во многих сетях работает нестабильно, поэтому дефолт — HTTP/2.
- Честный локальный подсчёт трафика по каждому аккаунту — сервер расход
  не сообщает (подробно: [QUOTA.md](QUOTA.md)).

## Как устроено

- **TUN**: `hev-socks5-tunnel` (snapshot upstream 2.19.x, MIT) лежит в
  репозитории — `android/third_party/hev-socks5-tunnel`, собирается через
  NDK вместе с приложением. Внешних источников нативного кода нет.
- **Мост**: hev отдаёт трафик в локальный SOCKS5, Go-ядро
  (`core/tunnel`) заворачивает его в HTTP/2 CONNECT к прокси Fastly
  (порт 2499) с ProxyPass-токеном Guardian. UDP уходит только в DNS.
- **DNS**: внутри туннеля — DNS-over-HTTPS (Cloudflare, RFC 8484),
  запасной путь — DNS-over-TCP. Прямого трафика на 1.1.1.1 не существует:
  адрес DNS в TUN виртуальный, запросы завершаются в ядре.
- **Токены**: OAuth FxA → ProxyPass JWT из `vpn.mozilla.org`
  (`core/fxa`, `core/guardian`), пул аккаунтов и квоты — `core/accounts`,
  мобильная обвязка для gomobile — `core/mobile`.

## Квота

Сервер при выдаче токена всегда присылает остаток, равный полному
лимиту, а прокси заголовков квоты не присылает вовсе. Поэтому расход
считает само приложение, серверные значения сливаются с локальным
учётом, а сброс происходит по дате `X-Quota-Reset`. Все факты и
решения — в [QUOTA.md](QUOTA.md).

## Сборка

APK собирает GitHub Actions (`.github/workflows/build-apk.yml`), готовый
файл — в релизе **v1.0.0-lite**:
<https://github.com/RendeRR/mzlvpn/releases/tag/v1.0.0-lite>

Для локальной сборки нужны JDK 17+, Android SDK/NDK и Go: ядро
компилируется в `core.aar` через gomobile, шрифты дизайна (Russo One,
IBM Plex Mono) скачивает gradle-задача `fetchDesignFonts` — в git они
не хранятся, при недоступности сети интерфейс откатывается на системные
шрифты.

## Структура репозитория

- `android/` — приложение: Kotlin/Compose UI, VpnService, JNI-обвязка hev.
- `core/` — Go-ядро: `fxa`, `guardian`, `accounts`, `tunnel`, `mobile`,
  `serverlist`.
- `src/`, `server.ts`, `index.html` — веб-мокап интерфейса (наследие
  прототипирования; продукт — Android-приложение).
- `QUOTA.md` — факты о квоте Mozilla и принятые решения по подсчёту.

## Дисклеймер

Неофициальный клиент, с Mozilla не аффилирован. Нужен аккаунт Mozilla
с доступом к VPN/FPN; клиент лишь использует публичные эндпоинты
Guardian/FxA от имени владельца аккаунта.
