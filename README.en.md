# Mozilla VPN (mzlvpn)

[Russian](README.md) · **English**

A lightweight unofficial Android client for Mozilla VPN / Firefox Private
Network. Native Kotlin + Jetpack Compose, with a Go networking core. The
"Instrument" design: paper, ink and a single signal orange — no gradients,
no round buttons.

## Screenshots

<p align="center">
  <img src="docs/screenshots/%D0%93%D0%BB%D0%B0%D0%B2%D0%BD%D1%8B%D0%B9%20%D1%8D%D0%BA%D1%80%D0%B0%D0%BD.png" width="260" alt="Home screen: connected, breaker switch ON">
  <img src="docs/screenshots/%D0%90%D0%BA%D0%BA%D0%B0%D1%83%D0%BD%D1%82%D1%8B.png" width="260" alt="Accounts: quotas, rotation, the Put-to-work button">
  <img src="docs/screenshots/%D0%9B%D0%BE%D0%BA%D0%B0%D1%86%D0%B8%D0%B8.png" width="260" alt="Locations: country list by ping">
</p>
<p align="center">Console · Accounts · Locations</p>

## Features

- **50 GB per month per account** — the quota the server issues to
  every Mozilla account; the app shows usage and remaining traffic
  from the server's own data. From November 10, 2026 through
  January 31, 2027, Mozilla has [uncapped data](https://blog.mozilla.org/en/firefox/built-in-vpn-unlimited-data/)
  for the free built-in VPN: during this period the server marks
  the quota as unlimited, and the app displays "unlimited traffic"
  instead of numbers and does not rotate accounts on quota.
- Mozilla account sign-in (FxA): password, email confirmation code and
  two-factor verification — right in the app.
- Multiple accounts with automatic rotation by monthly quota (threshold
  is adjustable, 95% by default) and pre-warming of the next account.
- 32 locations, with a "fastest" pick by real on-device ping.
- Two transports: **HTTP/2 CONNECT** (default) and HTTP/3 MASQUE.
  HTTP/3 (QUIC/UDP) is unreliable on many networks, hence the HTTP/2 default.
- **Home sites go direct**: addresses of a chosen country (20 countries,
  Russia by default) bypass the tunnel and load from your real IP
  address. Banks, government services and local sites see a home
  connection and work as usual, without the detour through the VPN;
  all other traffic stays in the tunnel. The core decides by
  destination IP; the country and the toggle live on the main screen.
- Quota as reported by the server: the app shows the limit and remaining
  quota from the Guardian response, and "unlimited traffic" instead of
  numbers while Mozilla's unlimited promo runs (details in
  [QUOTA.md](docs/QUOTA.md), in Russian).
- English and Russian UI; the language follows the system and is changed
  in the system settings (Settings → Languages → App languages) — there
  is no in-app language switch.

## How it works

- **TUN**: `hev-socks5-tunnel` (a snapshot of upstream 2.19.x, MIT) is
  vendored in the repo at `android/third_party/hev-socks5-tunnel` and
  built with the app via the NDK. No external native code sources.
- **Bridge**: hev hands traffic to a local SOCKS5; the Go core
  (`core/tunnel`) wraps it in HTTP/2 CONNECT to the Fastly proxy
  (port 2499) with a Guardian ProxyPass token. UDP goes to DNS only.
- **DNS**: inside the tunnel — DNS-over-HTTPS (Cloudflare, RFC 8484),
  with DNS-over-TCP as a fallback. There is no direct traffic to 1.1.1.1:
  the DNS address in the TUN is virtual, queries terminate in the core.
- **Tokens**: FxA OAuth → ProxyPass JWT from `vpn.mozilla.org`
  (`core/fxa`, `core/guardian`); the account pool and quotas live in
  `core/accounts`; the gomobile binding is `core/mobile`.

## Quota

The app adopts the quota snapshot from the Guardian response as-is:
limit, remaining quota, reset date and the `X-Quota-Unlimited` flag.
There is no local traffic counting; while Mozilla's unlimited promo
runs, the cards show "unlimited traffic" and quota-based rotation does
not trigger. All facts and decisions are in [QUOTA.md](docs/QUOTA.md)
(in Russian).

## Building

The APK is built by GitHub Actions (`.github/workflows/build-apk.yml`);
the ready file is on the releases page (current version — in `VERSION`):
<https://github.com/RendeRR/mzlvpn/releases>

A local build needs JDK 17+, the Android SDK/NDK and Go: the core is
compiled into `core.aar` via gomobile, and the design fonts (Russo One,
IBM Plex Mono) are downloaded by the `fetchDesignFonts` gradle task —
they are not stored in git, and the UI falls back to system fonts when
the network is unavailable.

## Repository layout

- `android/` — the app: Kotlin/Compose UI, VpnService, the hev JNI glue.
- `core/` — the Go core: `fxa`, `guardian`, `accounts`, `tunnel`, `geoip`,
  `mobile`, `serverlist`, `lang` (core message language).
- `docs/` — documentation: `QUOTA.md` (facts about Mozilla's quota and
  the counting decisions, in Russian), `IMPROVEMENTS.md` (the improvement
  log, in Russian) and screenshots.

## Disclaimer

An unofficial client, not affiliated with Mozilla. Requires a Mozilla
account with VPN/FPN access; the client merely uses the public
Guardian/FxA endpoints on behalf of the account owner.
