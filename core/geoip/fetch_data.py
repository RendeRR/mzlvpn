#!/usr/bin/env python3
"""Fetch aggregated per-country IPv4 prefix lists for the home-country
bypass (core/geoip). Source: ipdeny.com aggregated zones, which are
built from the five RIR delegated statistics and already aggregated
to minimal CIDR sets; we re-collapse defensively and store one CIDR
per line in data/<cc>.txt.

Run:  python3 fetch_data.py
The generated files are committed to the repo as a fallback; CI also
reruns this script before every build, so release APKs ship a fresh
database (a failed refresh falls back to the committed copy).
"""
import ipaddress
import os
import sys
import urllib.request

COUNTRIES = [
    "AM", "AZ", "BY", "DE", "EE", "FR", "GB", "GE", "KG", "KZ",
    "LT", "LV", "MD", "NL", "RU", "TJ", "TM", "UA", "US", "UZ",
]
URL = "https://www.ipdeny.com/ipblocks/data/aggregated/{cc}-aggregated.zone"
HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")

# Sanity anchors: (ip, must_be_in_country_or_None)
CHECKS = [
    ("77.88.8.8", "RU"),      # Yandex DNS
    ("93.158.134.3", "RU"),   # yandex.ru
    ("217.69.139.202", "RU"), # mail.ru
    ("8.8.8.8", "US"),        # Google DNS
    ("1.1.1.1", None),        # Cloudflare (AU-allocated anycast)
]


def fetch(cc):
    req = urllib.request.Request(URL.format(cc=cc.lower()),
                                 headers={"User-Agent": "mzlvpn-geoip-fetch"})
    with urllib.request.urlopen(req, timeout=30) as r:
        text = r.read().decode("ascii")
    nets = []
    for line in text.splitlines():
        line = line.strip()
        if line:
            nets.append(ipaddress.ip_network(line))
    merged = list(ipaddress.collapse_addresses(nets))
    merged.sort(key=lambda n: int(n.network_address))
    return merged


def main():
    os.makedirs(DATA, exist_ok=True)
    tables = {}
    total = 0
    for cc in COUNTRIES:
        nets = fetch(cc)
        tables[cc] = nets
        total += len(nets)
        path = os.path.join(DATA, cc.lower() + ".txt")
        with open(path, "w") as f:
            f.write(f"# {cc} IPv4 prefixes, aggregated.\n")
            f.write("# Source: ipdeny.com aggregated zones (RIR delegated data).\n")
            for n in nets:
                f.write(str(n) + "\n")
        print(f"{cc}: {len(nets)} prefixes")
    print(f"total: {total} prefixes")

    for ip_s, want in CHECKS:
        ip = ipaddress.ip_address(ip_s)
        got = None
        for cc, nets in tables.items():
            if any(ip in n for n in nets):
                got = cc
                break
        status = "OK" if got == want else "FAIL"
        print(f"check {ip_s}: in {got}, want {want} -> {status}")
        if got != want:
            sys.exit(1)


if __name__ == "__main__":
    main()
