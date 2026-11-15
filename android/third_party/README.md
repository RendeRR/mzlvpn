# Vendored third-party code

## hev-socks5-tunnel

The TUN → SOCKS5 forwarder library (C, built by the NDK into
`libhev-socks5-tunnel.so`). It reads packets from the Android TUN fd,
splits TCP/UDP flows and opens SOCKS5 connections to the Go core.

- Upstream: hev-socks5-tunnel by Heiher (https://github.com/heiher/hev-socks5-tunnel)
- License: MIT — see `hev-socks5-tunnel/LICENSE` (Copyright (c) 2022 hev)
- Snapshot: copied verbatim on 2026-11-15 from
  armanlife/arman-vpn-client@405f412fa28c0f56bd2a0d85617558e7216d6b05
  (path `app/android/hev-socks5-tunnel`), which vendors upstream 2.19.x
  (the build passes `REV_ID=2.19.3`).

Before this snapshot existed, CI cloned armanlife/arman-vpn-client on
every build and copied the tree from there — an unpinned dependency on
someone else's repository. The vendored copy is now the single source
used by Gradle (`android/app/build.gradle.kts` points at
`../third_party/hev-socks5-tunnel/Android.mk`).

To update: replace the whole directory with a newer upstream tree,
keep this note in sync, and re-check the JNI contract in
`hev-jni.c` (PKGNAME/CLSNAME defines and the four TProxy* method
signatures) against `app/mozillavpnlite/Tun2Socks.kt`.
