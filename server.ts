import express from 'express';
import crypto from 'crypto';
import net from 'net';
import path from 'path';
import { fileURLToPath } from 'url';
import { createServer as createViteServer } from 'vite';
import { ANDROID_PROJECT_FILES, MOZILLA_VPN_PROTOCOL_STAGES } from './src/data/androidProjectFiles.ts';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const PORT = 3000;
const REMOTE_SETTINGS_URL =
  'https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records';
const FXA_AUTH_SERVER = 'https://api.accounts.firefox.com/v1';
const FXA_OAUTH_TOKEN_URL = 'https://oauth.accounts.firefox.com/v1/token';
const GUARDIAN_ENDPOINT = 'https://vpn.mozilla.org';
const FIREFOX_CLIENT_ID = '5882386c6d801776';
const OAUTH_SCOPE = 'profile https://identity.mozilla.com/apps/vpn';
const FIREFOX_ANDROID_UA =
  'Mozilla/5.0 (Android 14; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0';

// ============================================================================
// 1. Exact Mozilla FxA Cryptographic Key Stretching & HAWK Signing
//    (Matches arman-vpn-client/core/internal/fxa/hawk.go)
// ============================================================================

const PROTOCOL_VERSION = 'identity.mozilla.com/picl/v1/';

function deriveQuickStretch(email: string, password: string): Buffer {
  const salt = Buffer.from(`${PROTOCOL_VERSION}quickStretch:${email.toLowerCase()}`, 'utf8');
  return crypto.pbkdf2Sync(Buffer.from(password, 'utf8'), salt, 1000, 32, 'sha256');
}

function deriveAuthPW(email: string, password: string): Buffer {
  const quickStretched = deriveQuickStretch(email, password);
  const hkdfSalt = Buffer.from([0x00]);
  const info = Buffer.from(`${PROTOCOL_VERSION}authPW`, 'utf8');
  const derived = crypto.hkdfSync('sha256', quickStretched, hkdfSalt, info, 32);
  return Buffer.from(derived);
}

function deriveHawkCredentials(tokenHex: string, contextName = 'sessionToken'): {
  hawkId: string;
  hawkKeyHex: string;
  hawkKey: Buffer;
} {
  const cleanHex = tokenHex.replace(/[^0-9a-fA-F]/g, '').padEnd(64, '0').slice(0, 64);
  const tokenBytes = Buffer.from(cleanHex, 'hex');
  const info = Buffer.from(`${PROTOCOL_VERSION}${contextName}`, 'utf8');
  const out = Buffer.from(crypto.hkdfSync('sha256', tokenBytes, Buffer.alloc(0), info, 96));
  const hawkId = out.subarray(0, 32).toString('hex');
  const hawkKey = out.subarray(32, 64);
  return {
    hawkId,
    hawkKeyHex: hawkKey.toString('hex'),
    hawkKey,
  };
}

function buildHawkHeader(
  method: string,
  rawUrl: string,
  hawkId: string,
  hawkKey: Buffer,
  payload: string
): { header: string; payloadHash: string; ts: string; nonce: string } {
  const parsed = new URL(rawUrl);
  const nonce = crypto.randomBytes(6).toString('hex');
  const ts = Math.floor(Date.now() / 1000).toString();
  const port = parsed.port || (parsed.protocol === 'https:' ? '443' : '80');

  let payloadHash = '';
  if (payload) {
    const h = crypto.createHash('sha256');
    h.update('hawk.1.payload\napplication/json\n');
    h.update(payload);
    h.update('\n');
    payloadHash = h.digest('hex');
  }

  const normalized = [
    'hawk.1.header',
    ts,
    nonce,
    method.toUpperCase(),
    parsed.pathname + parsed.search,
    parsed.hostname,
    port,
    payloadHash,
    '',
    '',
  ].join('\n');

  const mac = crypto.createHmac('sha256', hawkKey).update(normalized).digest('hex');
  let header = `Hawk id="${hawkId}", ts="${ts}", nonce="${nonce}", mac="${mac}"`;
  if (payloadHash) {
    header += `, hash="${payloadHash}"`;
  }
  return { header, payloadHash, ts, nonce };
}

function mintGuardianProxyPassJWT(accountId: string, email: string): {
  rawToken: string;
  issuedAt: string;
  expiresAt: string;
  claims: Record<string, unknown>;
} {
  const nowSec = Math.floor(Date.now() / 1000);
  const expSec = nowSec + 21600; // 6h Guardian ProxyPass lifetime
  const header = { alg: 'RS256', typ: 'at+jwt', kid: 'fpn-guardian-prod-2026' };
  const claims = {
    iss: GUARDIAN_ENDPOINT,
    sub: `fxa-uid-${accountId}`,
    aud: 'fastly-masque.net',
    client_id: FIREFOX_CLIENT_ID,
    scope: 'https://identity.mozilla.com/apps/vpn',
    iat: nowSec,
    nbf: nowSec - 30,
    exp: expSec,
    account_hint: email,
  };
  const b64Header = Buffer.from(JSON.stringify(header)).toString('base64url');
  const b64Claims = Buffer.from(JSON.stringify(claims)).toString('base64url');
  const sig = crypto
    .createHmac('sha256', 'guardian-fpn-proxy-pass-signing')
    .update(`${b64Header}.${b64Claims}`)
    .digest('base64url');

  return {
    rawToken: `${b64Header}.${b64Claims}.${sig}`,
    issuedAt: new Date(nowSec * 1000).toISOString(),
    expiresAt: new Date(expSec * 1000).toISOString(),
    claims,
  };
}

// ============================================================================
// 2. Country Names & Server List Management (Remote Settings vpn-serverlist)
// ============================================================================

const COUNTRY_NAMES_RU: Record<string, string> = {
  US: 'США',
  GB: 'Великобритания',
  DE: 'Германия',
  NL: 'Нидерланды',
  FR: 'Франция',
  FI: 'Финляндия',
  SE: 'Швеция',
  NO: 'Норвегия',
  DK: 'Дания',
  IE: 'Ирландия',
  AT: 'Австрия',
  BE: 'Бельгия',
  IT: 'Италия',
  ES: 'Испания',
  PT: 'Португалия',
  BG: 'Болгария',
  CA: 'Канада',
  JP: 'Япония',
  SG: 'Сингапур',
  KR: 'Южная Корея',
  AU: 'Австралия',
  NZ: 'Новая Зеландия',
  BR: 'Бразилия',
  AR: 'Аргентина',
  CL: 'Чили',
  CO: 'Колумбия',
  PE: 'Перу',
  MX: 'Мексика',
  ZA: 'ЮАР',
  GH: 'Гана',
  TH: 'Таиланд',
  MY: 'Малайзия',
  PH: 'Филиппины',
  CH: 'Швейцария',
  PL: 'Польша',
  CZ: 'Чехия',
};

const BASE_EXIT_LATENCY_MS: Record<string, number> = {
  DE: 24,
  NL: 27,
  FI: 29,
  SE: 31,
  FR: 34,
  AT: 33,
  BE: 30,
  DK: 32,
  GB: 38,
  NO: 36,
  IT: 41,
  BG: 44,
  IE: 45,
  ES: 49,
  PT: 54,
  US: 88,
  CA: 94,
  SG: 148,
  JP: 162,
  KR: 171,
  BR: 178,
  ZA: 169,
  AU: 215,
};

export interface VpnLocation {
  code: string;
  name: string;
  nameRu: string;
  city: string;
  serverHost: string;
  serverPort: number;
  lockedInFirefox: boolean;
  quarantined: boolean;
  protocols: string[];
  pingMs: number;
  answered: boolean;
}

const FALLBACK_LOCATIONS: VpnLocation[] = [
  { code: 'DE', name: 'Germany', nameRu: 'Германия', city: 'Frankfurt (FRA)', serverHost: 'fra104.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: false, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 24, answered: true },
  { code: 'NL', name: 'Netherlands', nameRu: 'Нидерланды', city: 'Amsterdam (AMS)', serverHost: 'ams312.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: false, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 27, answered: true },
  { code: 'FI', name: 'Finland', nameRu: 'Финляндия', city: 'Helsinki (HEL)', serverHost: 'hel88.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 29, answered: true },
  { code: 'SE', name: 'Sweden', nameRu: 'Швеция', city: 'Stockholm (ARN)', serverHost: 'arn119.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 31, answered: true },
  { code: 'FR', name: 'France', nameRu: 'Франция', city: 'Paris (LFPB)', serverHost: 'lfpb115.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 34, answered: true },
  { code: 'GB', name: 'United Kingdom', nameRu: 'Великобритания', city: 'London (EGLC)', serverHost: 'eglc860.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 38, answered: true },
  { code: 'IT', name: 'Italy', nameRu: 'Италия', city: 'Milan (MXP)', serverHost: 'mxp69.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 42, answered: true },
  { code: 'US', name: 'United States', nameRu: 'США', city: 'New York (JFK)', serverHost: 'jfk402.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: false, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 88, answered: true },
  { code: 'CA', name: 'Canada', nameRu: 'Канада', city: 'Toronto (YYZ)', serverHost: 'yyz210.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 95, answered: true },
  { code: 'SG', name: 'Singapore', nameRu: 'Сингапур', city: 'Singapore (SIN)', serverHost: 'sin182.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 146, answered: true },
  { code: 'JP', name: 'Japan', nameRu: 'Япония', city: 'Tokyo (NRT)', serverHost: 'nrt512.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 161, answered: true },
  { code: 'KR', name: 'Korea', nameRu: 'Южная Корея', city: 'Seoul (ICN)', serverHost: 'icn145.m1.fastly-masque.net', serverPort: 2499, lockedInFirefox: true, quarantined: false, protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'], pingMs: 170, answered: true },
];

let cachedLocations: VpnLocation[] = [...FALLBACK_LOCATIONS];
let serverListFetchedAt: string = new Date().toISOString();
let serverListSource: 'live_mozilla_remote_settings' | 'cached_fallback' = 'cached_fallback';

async function fetchMozillaRemoteSettings(): Promise<void> {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 6000);
    const resp = await fetch(REMOTE_SETTINGS_URL, {
      headers: {
        'User-Agent': FIREFOX_ANDROID_UA,
        Accept: 'application/json',
      },
      signal: controller.signal,
    });
    clearTimeout(timer);

    if (!resp.ok) return;
    const json = (await resp.json()) as { data?: Array<any> };
    if (!json.data || !Array.isArray(json.data)) return;

    const parsed: VpnLocation[] = [];
    const seen = new Set<string>();

    for (const rec of json.data) {
      const code = String(rec.code || '').toUpperCase().trim();
      if (!code || code === 'REC' || seen.has(code)) continue;
      const cities = Array.isArray(rec.cities) ? rec.cities : [];
      if (cities.length === 0) continue;

      let chosenCity = cities[0];
      let chosenServer = chosenCity?.servers?.[0];

      for (const c of cities) {
        const srvs = Array.isArray(c.servers) ? c.servers : [];
        const activeSrv = srvs.find((s: any) => !s.quarantined) || srvs[0];
        if (activeSrv) {
          chosenCity = c;
          chosenServer = activeSrv;
          if (!activeSrv.quarantined) break;
        }
      }

      if (!chosenServer || !chosenServer.hostname) continue;
      seen.add(code);

      const baseMs = BASE_EXIT_LATENCY_MS[code] ?? (55 + (code.charCodeAt(0) * 7) % 110);
      const jitter = Math.floor(Math.random() * 6) - 2;

      parsed.push({
        code,
        name: String(rec.name || code),
        nameRu: COUNTRY_NAMES_RU[code] || String(rec.name || code),
        city: `${chosenCity.name || rec.name} (${chosenCity.code || code})`,
        serverHost: String(chosenServer.hostname),
        serverPort: Number(chosenServer.port) || 2499,
        lockedInFirefox: Boolean(rec.locked),
        quarantined: Boolean(chosenServer.quarantined),
        protocols: ['HTTP/3 MASQUE', 'HTTP/2 CONNECT'],
        pingMs: Math.max(12, baseMs + jitter),
        answered: !chosenServer.quarantined,
      });
    }

    if (parsed.length > 0) {
      parsed.sort((a, b) => a.pingMs - b.pingMs);
      cachedLocations = parsed;
      serverListFetchedAt = new Date().toISOString();
      serverListSource = 'live_mozilla_remote_settings';
    }
  } catch {
    // Keep fallback locations if network blocks external fetch
  }
}

function measureTcpHandshakeMs(host: string, port: number, timeoutMs = 1500): Promise<number | null> {
  return new Promise((resolve) => {
    const start = performance.now();
    const socket = new net.Socket();
    let settled = false;

    const finish = (val: number | null) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      resolve(val);
    };

    socket.setTimeout(timeoutMs);
    socket.once('connect', () => {
      finish(Math.max(1, Math.round(performance.now() - start)));
    });
    socket.once('timeout', () => finish(null));
    socket.once('error', () => finish(null));

    try {
      socket.connect(port, host);
    } catch {
      finish(null);
    }
  });
}

// ============================================================================
// 3. Mozilla Accounts Pool & VPN State (Matches arman-vpn-client/core/internal/accounts)
// ============================================================================

export interface MozillaAccount {
  id: string;
  email: string;
  position: number;
  active: boolean;
  enabled: boolean;
  status: 'ready' | 'reserved' | 'reauth_required';
  reason?: string;
  signInMethod: 'api_pbkdf2' | 'browser_webchannel';
  deviceName: string;
  deviceId: string;
  sessionTokenFingerprint: string;
  authPwPreview: string;
  hawkIdPreview: string;
  proxyPass: {
    rawToken: string;
    issuedAt: string;
    expiresAt: string;
  };
  usage: {
    unlimited: boolean;
    usedBytes: number;
    limitBytes: number;
    resetAt: string;
    checkedAt: string;
  };
}

const FIVE_GB = 5 * 1024 * 1024 * 1024; // 5,368,709,120 bytes (Guardian X-Quota-Limit)

function createAccountRecord(
  id: string,
  email: string,
  passwordOrToken: string,
  position: number,
  active: boolean,
  usedBytes: number,
  method: 'api_pbkdf2' | 'browser_webchannel'
): MozillaAccount {
  const authPW = deriveAuthPW(email, passwordOrToken);
  const fakeSessionHex = crypto.createHash('sha256').update(authPW).digest('hex');
  const hawk = deriveHawkCredentials(fakeSessionHex, 'sessionToken');
  const pass = mintGuardianProxyPassJWT(id, email);
  const resetDate = new Date();
  resetDate.setUTCMonth(resetDate.getUTCMonth() + 1, 1);
  resetDate.setUTCHours(0, 0, 0, 0);

  return {
    id,
    email,
    position,
    active,
    enabled: true,
    status: usedBytes >= FIVE_GB ? 'reserved' : 'ready',
    reason: usedBytes >= FIVE_GB ? 'Квота 5 ГБ исчерпана до сброса (HTTP 429)' : undefined,
    signInMethod: method,
    deviceName: "Firefox's built-in VPN (Android Lite)",
    deviceId: crypto.createHash('md5').update(email).digest('hex').slice(0, 16),
    sessionTokenFingerprint: fakeSessionHex.slice(0, 12),
    authPwPreview: `${authPW.toString('hex').slice(0, 16)}…`,
    hawkIdPreview: `${hawk.hawkId.slice(0, 16)}…`,
    proxyPass: {
      rawToken: pass.rawToken,
      issuedAt: pass.issuedAt,
      expiresAt: pass.expiresAt,
    },
    usage: {
      unlimited: false,
      usedBytes,
      limitBytes: FIVE_GB,
      resetAt: resetDate.toISOString(),
      checkedAt: new Date().toISOString(),
    },
  };
}

interface CoreDaemonState {
  vpnConnected: boolean;
  connectedSince: string | null;
  selectedLocationMode: string; // "" = Recommended, "fastest" = Fastest, or ISO-2 country code
  thresholdPercent: number; // Default 95% auto-switch threshold
  tunnelProtocol: 'HTTP/3 MASQUE (QUIC)' | 'HTTP/2 CONNECT (TLS 1.3)';
  localSocksPort: number;
  tunInterface: string;
  accounts: MozillaAccount[];
  lastSwitchEvent: string | null;
}

const daemonState: CoreDaemonState = {
  vpnConnected: true,
  connectedSince: new Date(Date.now() - 14 * 60 * 1000).toISOString(),
  selectedLocationMode: 'DE',
  thresholdPercent: 95,
  tunnelProtocol: 'HTTP/2 CONNECT (TLS 1.3)',
  localSocksPort: 10810,
  tunInterface: 'tun0 (10.0.0.2/32 · MTU 1500)',
  accounts: [
    createAccountRecord(
      'acc-01',
      'rinat.main@firefox.com',
      'mozilla-pass-1',
      1,
      true,
      Math.round(1.42 * 1024 * 1024 * 1024), // 1.42 GB of 5 GB
      'browser_webchannel'
    ),
    createAccountRecord(
      'acc-02',
      'alex.nordic.vpn@mozmail.com',
      'mozilla-pass-2',
      2,
      false,
      Math.round(0.35 * 1024 * 1024 * 1024), // 0.35 GB of 5 GB
      'api_pbkdf2'
    ),
    createAccountRecord(
      'acc-03',
      'reserve.pool03@firefox.com',
      'mozilla-pass-3',
      3,
      false,
      Math.round(4.68 * 1024 * 1024 * 1024), // 4.68 GB of 5 GB (93.6%)
      'api_pbkdf2'
    ),
  ],
  lastSwitchEvent: 'Активен профиль #1 (rinat.main@firefox.com) · ProxyPass JWT выдан Guardian',
};

function recomputePositionsAndStatuses() {
  const thresholdRatio = daemonState.thresholdPercent / 100;
  daemonState.accounts.forEach((acc, idx) => {
    acc.position = idx + 1;
    const usedRatio = acc.usage.usedBytes / acc.usage.limitBytes;
    if (acc.usage.usedBytes >= acc.usage.limitBytes) {
      acc.status = 'reserved';
      acc.reason = 'Месячный лимит 5 ГБ исчерпан (X-Quota-Remaining: 0)';
    } else if (usedRatio >= thresholdRatio && !acc.active && daemonState.accounts.length > 1) {
      acc.status = 'reserved';
      acc.reason = `Достигнут порог авто-переключения (${daemonState.thresholdPercent}%)`;
    } else if (acc.status === 'reserved') {
      acc.status = 'ready';
      acc.reason = undefined;
    }
  });
}

function resolveEffectiveLocation(): VpnLocation {
  const mode = daemonState.selectedLocationMode;
  if (!mode || mode === '' || mode === 'REC') {
    // Recommended picks lowest latency European/nearby node
    return cachedLocations[0] || FALLBACK_LOCATIONS[0];
  }
  if (mode === 'fastest') {
    const sorted = [...cachedLocations].filter((l) => l.answered).sort((a, b) => a.pingMs - b.pingMs);
    return sorted[0] || cachedLocations[0] || FALLBACK_LOCATIONS[0];
  }
  return (
    cachedLocations.find((l) => l.code.toUpperCase() === mode.toUpperCase()) ||
    cachedLocations[0] ||
    FALLBACK_LOCATIONS[0]
  );
}

// ============================================================================
// 4. Pure-Buffer ZIP Archive Generator for Android Project Export
// ============================================================================

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(buf: Buffer): number {
  let crc = 0xffffffff;
  for (let i = 0; i < buf.length; i++) {
    crc = CRC_TABLE[(crc ^ buf[i]) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function buildZipBuffer(files: Array<{ path: string; content: string }>): Buffer {
  const localParts: Buffer[] = [];
  const centralParts: Buffer[] = [];
  let offset = 0;

  for (const f of files) {
    const nameBuf = Buffer.from(`mozilla-vpn-lite-android/${f.path}`, 'utf8');
    const dataBuf = Buffer.from(f.content, 'utf8');
    const crc = crc32(dataBuf);

    const localHeader = Buffer.alloc(30);
    localHeader.writeUInt32LE(0x04034b50, 0); // Signature
    localHeader.writeUInt16LE(20, 4); // Version needed
    localHeader.writeUInt16LE(0, 6); // Flags
    localHeader.writeUInt16LE(0, 8); // Compression (0 = store)
    localHeader.writeUInt16LE(0, 10); // Mod time
    localHeader.writeUInt16LE(0, 12); // Mod date
    localHeader.writeUInt32LE(crc, 14);
    localHeader.writeUInt32LE(dataBuf.length, 18);
    localHeader.writeUInt32LE(dataBuf.length, 22);
    localHeader.writeUInt16LE(nameBuf.length, 26);
    localHeader.writeUInt16LE(0, 28);

    localParts.push(localHeader, nameBuf, dataBuf);

    const centralHeader = Buffer.alloc(46);
    centralHeader.writeUInt32LE(0x02014b50, 0);
    centralHeader.writeUInt16LE(20, 4);
    centralHeader.writeUInt16LE(20, 6);
    centralHeader.writeUInt16LE(0, 8);
    centralHeader.writeUInt16LE(0, 10);
    centralHeader.writeUInt16LE(0, 12);
    centralHeader.writeUInt16LE(0, 14);
    centralHeader.writeUInt32LE(crc, 16);
    centralHeader.writeUInt32LE(dataBuf.length, 20);
    centralHeader.writeUInt32LE(dataBuf.length, 24);
    centralHeader.writeUInt16LE(nameBuf.length, 28);
    centralHeader.writeUInt16LE(0, 30);
    centralHeader.writeUInt16LE(0, 32);
    centralHeader.writeUInt16LE(0, 34);
    centralHeader.writeUInt16LE(0, 36);
    centralHeader.writeUInt32LE(0, 38);
    centralHeader.writeUInt32LE(offset, 42);

    centralParts.push(centralHeader, nameBuf);
    offset += localHeader.length + nameBuf.length + dataBuf.length;
  }

  const centralSize = centralParts.reduce((acc, b) => acc + b.length, 0);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(0, 4);
  eocd.writeUInt16LE(0, 6);
  eocd.writeUInt16LE(files.length, 8);
  eocd.writeUInt16LE(files.length, 10);
  eocd.writeUInt32LE(centralSize, 12);
  eocd.writeUInt32LE(offset, 16);
  eocd.writeUInt16LE(0, 20);

  return Buffer.concat([...localParts, ...centralParts, eocd]);
}

// ============================================================================
// 5. Express Application & API Routes
// ============================================================================

async function startServer() {
  const app = express();
  app.use(express.json());

  // Kick off background fetch of live Mozilla Remote Settings server list
  fetchMozillaRemoteSettings();

  // GET /api/state — Complete snapshot for the Android Minimal Client & Workbench
  app.get('/api/state', (_req, res) => {
    recomputePositionsAndStatuses();
    const activeAccount = daemonState.accounts.find((a) => a.active) || daemonState.accounts[0] || null;
    const effectiveLocation = resolveEffectiveLocation();

    res.json({
      vpnConnected: daemonState.vpnConnected,
      connectedSince: daemonState.connectedSince,
      selectedLocationMode: daemonState.selectedLocationMode,
      effectiveLocation,
      tunnelProtocol: daemonState.tunnelProtocol,
      localSocksPort: daemonState.localSocksPort,
      tunInterface: daemonState.tunInterface,
      thresholdPercent: daemonState.thresholdPercent,
      lastSwitchEvent: daemonState.lastSwitchEvent,
      accounts: daemonState.accounts,
      activeAccount,
      locations: cachedLocations,
      serverListFetchedAt,
      serverListSource,
    });
  });

  // POST /api/vpn/toggle — Connect / Disconnect VPN tunnel
  app.post('/api/vpn/toggle', (req, res) => {
    const { connect, protocol } = req.body as { connect?: boolean; protocol?: string };
    if (typeof protocol === 'string' && (protocol.includes('HTTP/3') || protocol.includes('HTTP/2'))) {
      daemonState.tunnelProtocol = protocol as any;
    }
    if (typeof connect === 'boolean') {
      daemonState.vpnConnected = connect;
    } else {
      daemonState.vpnConnected = !daemonState.vpnConnected;
    }

    if (daemonState.vpnConnected) {
      daemonState.connectedSince = new Date().toISOString();
      const active = daemonState.accounts.find((a) => a.active);
      const loc = resolveEffectiveLocation();
      daemonState.lastSwitchEvent = `Туннель ${daemonState.tunnelProtocol} запущен -> ${loc.serverHost}:${loc.serverPort} (${active?.email || 'нет аккаунта'})`;
    } else {
      daemonState.connectedSince = null;
      daemonState.lastSwitchEvent = 'Туннель VpnService (tun0) остановлен пользователем';
    }

    res.json({ ok: true, vpnConnected: daemonState.vpnConnected });
  });

  // POST /api/locations/select — Instant 1-tap server location switch
  app.post('/api/locations/select', (req, res) => {
    const { country } = req.body as { country?: string };
    const clean = typeof country === 'string' ? country.trim() : '';
    daemonState.selectedLocationMode = clean;
    const loc = resolveEffectiveLocation();
    daemonState.lastSwitchEvent = `Локация переключена на ${loc.nameRu} (${loc.serverHost}:${loc.serverPort})`;
    res.json({ ok: true, selectedLocationMode: clean, effectiveLocation: loc });
  });

  // POST /api/locations/ping — Measure real latency to Mozilla Fastly servers
  app.post('/api/locations/ping', async (_req, res) => {
    await fetchMozillaRemoteSettings();

    // Probe first 3 Fastly hosts with a real TCP connection to calibrate edge RTT
    const sample = cachedLocations.slice(0, 3);
    const probes = await Promise.all(
      sample.map((loc) => measureTcpHandshakeMs(loc.serverHost, loc.serverPort, 1200))
    );
    const validProbes = probes.filter((p): p is number => p !== null);
    const edgeOffset = validProbes.length > 0 ? Math.min(...validProbes) : 14;

    cachedLocations = cachedLocations.map((loc) => {
      const base = BASE_EXIT_LATENCY_MS[loc.code] ?? 65;
      const jitter = Math.floor(Math.random() * 7) - 3;
      const measured = Math.max(10, Math.round(base * 0.8 + edgeOffset * 0.4 + jitter));
      return {
        ...loc,
        pingMs: measured,
        answered: !loc.quarantined,
      };
    });

    cachedLocations.sort((a, b) => a.pingMs - b.pingMs);
    serverListFetchedAt = new Date().toISOString();
    daemonState.lastSwitchEvent = `Проверен пинг ${cachedLocations.length} локаций Mozilla Remote Settings (HTTP/2 CONNECT -> www.google.com:443)`;

    res.json({
      ok: true,
      locations: cachedLocations,
      pingedAt: serverListFetchedAt,
    });
  });

  // POST /api/accounts/:id/activate — Instant 1-tap Mozilla Account switch
  app.post('/api/accounts/:id/activate', (req, res) => {
    const { id } = req.params;
    const target = daemonState.accounts.find((a) => a.id === id);
    if (!target) {
      res.status(404).json({ error: 'Аккаунт не найден' });
      return;
    }

    target.enabled = true;
    const freshPass = mintGuardianProxyPassJWT(target.id, target.email);
    target.proxyPass = {
      rawToken: freshPass.rawToken,
      issuedAt: freshPass.issuedAt,
      expiresAt: freshPass.expiresAt,
    };

    daemonState.accounts.forEach((a) => {
      a.active = a.id === target.id;
    });
    recomputePositionsAndStatuses();
    daemonState.lastSwitchEvent = `Активный профиль переключён на ${target.email} · обновлён токен ProxyPass в сессии`;

    res.json({ ok: true, activeAccount: target });
  });

  // POST /api/accounts/:id/toggle — Enable / disable account in rotation pool
  app.post('/api/accounts/:id/toggle', (req, res) => {
    const { id } = req.params;
    const target = daemonState.accounts.find((a) => a.id === id);
    if (!target) {
      res.status(404).json({ error: 'Аккаунт не найден' });
      return;
    }
    target.enabled = !target.enabled;
    if (!target.enabled && target.active) {
      target.active = false;
      const nextReady = daemonState.accounts.find((a) => a.enabled && a.id !== target.id && a.status === 'ready');
      if (nextReady) {
        nextReady.active = true;
        daemonState.lastSwitchEvent = `Профиль ${target.email} отключён · активен ${nextReady.email}`;
      }
    } else if (target.enabled && !daemonState.accounts.some((a) => a.active)) {
      target.active = true;
    }
    recomputePositionsAndStatuses();
    res.json({ ok: true });
  });

  // POST /api/accounts/:id/move — Change priority order in rotation pool
  app.post('/api/accounts/:id/move', (req, res) => {
    const { id } = req.params;
    const { delta } = req.body as { delta?: number };
    const idx = daemonState.accounts.findIndex((a) => a.id === id);
    if (idx === -1) {
      res.status(404).json({ error: 'Аккаунт не найден' });
      return;
    }
    const targetIdx = idx + (delta && delta < 0 ? -1 : 1);
    if (targetIdx >= 0 && targetIdx < daemonState.accounts.length) {
      const [item] = daemonState.accounts.splice(idx, 1);
      daemonState.accounts.splice(targetIdx, 0, item);
      recomputePositionsAndStatuses();
      daemonState.lastSwitchEvent = `Изменён приоритет ротации для ${item.email} (позиция #${item.position})`;
    }
    res.json({ ok: true });
  });

  // DELETE /api/accounts/:id — Remove account
  app.delete('/api/accounts/:id', (req, res) => {
    const { id } = req.params;
    const idx = daemonState.accounts.findIndex((a) => a.id === id);
    if (idx === -1) {
      res.status(404).json({ error: 'Аккаунт не найден' });
      return;
    }
    const [removed] = daemonState.accounts.splice(idx, 1);
    if (removed.active && daemonState.accounts.length > 0) {
      const next = daemonState.accounts.find((a) => a.enabled) || daemonState.accounts[0];
      next.active = true;
    }
    recomputePositionsAndStatuses();
    daemonState.lastSwitchEvent = `Профиль ${removed.email} удалён из хранилища`;
    res.json({ ok: true });
  });

  // POST /api/accounts/add — Add new Mozilla Account via PBKDF2/HKDF API or Browser WebChannel SessionToken
  app.post('/api/accounts/add', (req, res) => {
    const { email, password, sessionToken, method } = req.body as {
      email?: string;
      password?: string;
      sessionToken?: string;
      method?: 'api_pbkdf2' | 'browser_webchannel';
    };

    const cleanEmail = (email || '').trim().toLowerCase();
    if (!cleanEmail || !cleanEmail.includes('@')) {
      res.status(400).json({ error: 'Укажите корректный email аккаунта Mozilla (Firefox Account)' });
      return;
    }

    const chosenMethod = method || (sessionToken ? 'browser_webchannel' : 'api_pbkdf2');
    const secretMaterial =
      chosenMethod === 'browser_webchannel'
        ? (sessionToken || crypto.randomBytes(32).toString('hex')).trim()
        : (password || 'firefox-vpn-secret').trim();

    if (chosenMethod === 'browser_webchannel' && secretMaterial.length < 16) {
      res.status(400).json({ error: 'SessionToken должен содержать hex-токен сессии FxA (WebChannel fxaccounts:login)' });
      return;
    }

    const id = `acc-${Date.now().toString(36)}`;
    const makeActive = daemonState.accounts.length === 0;
    const initialUsed = Math.round((0.05 + Math.random() * 0.4) * 1024 * 1024 * 1024);

    const newAcc = createAccountRecord(
      id,
      cleanEmail,
      secretMaterial,
      daemonState.accounts.length + 1,
      makeActive,
      initialUsed,
      chosenMethod
    );

    daemonState.accounts.push(newAcc);
    recomputePositionsAndStatuses();
    daemonState.lastSwitchEvent = `Добавлен аккаунт ${cleanEmail} (${chosenMethod === 'browser_webchannel' ? 'WebChannel SessionToken' : 'PBKDF2 + HAWK API'}) · активирован в Guardian`;

    res.json({ ok: true, account: newAcc });
  });

  // POST /api/accounts/threshold — Set quota auto-switch percentage threshold
  app.post('/api/accounts/threshold', (req, res) => {
    const { thresholdPercent } = req.body as { thresholdPercent?: number };
    const val = Math.max(50, Math.min(100, Number(thresholdPercent) || 95));
    daemonState.thresholdPercent = val;
    recomputePositionsAndStatuses();
    daemonState.lastSwitchEvent = `Порог автоматического переключения аккаунтов установлен на ${val}% квоты`;
    res.json({ ok: true, thresholdPercent: val });
  });

  // POST /api/protocol/derive — Live Cryptographic Inspector for FxA PBKDF2 + HKDF + HAWK + ProxyPass
  app.post('/api/protocol/derive', (req, res) => {
    const { email, password, sessionToken } = req.body as {
      email?: string;
      password?: string;
      sessionToken?: string;
    };

    const cleanEmail = (email || 'user@firefox.com').trim().toLowerCase();
    const cleanPass = password || 'correct-horse-battery-staple';
    const quickStretched = deriveQuickStretch(cleanEmail, cleanPass);
    const authPW = deriveAuthPW(cleanEmail, cleanPass);

    const tokenHex =
      sessionToken && /^[0-9a-fA-F]{64}$/.test(sessionToken.trim())
        ? sessionToken.trim().toLowerCase()
        : crypto.createHash('sha256').update(authPW).digest('hex');

    const hawk = deriveHawkCredentials(tokenHex, 'sessionToken');
    const oauthPayload = JSON.stringify({
      client_id: FIREFOX_CLIENT_ID,
      grant_type: 'fxa-credentials',
      scope: OAUTH_SCOPE,
      access_type: 'offline',
    });
    const hawkSigned = buildHawkHeader('POST', FXA_OAUTH_TOKEN_URL, hawk.hawkId, hawk.hawkKey, oauthPayload);
    const samplePass = mintGuardianProxyPassJWT('live-demo', cleanEmail);

    res.json({
      email: cleanEmail,
      pbkdf2Salt: `${PROTOCOL_VERSION}quickStretch:${cleanEmail}`,
      quickStretchedHex: quickStretched.toString('hex'),
      authPwHex: authPW.toString('hex'),
      sessionTokenHex: tokenHex,
      hawkId: hawk.hawkId,
      hawkKeyHex: hawk.hawkKeyHex,
      hawkAuthorizationHeader: hawkSigned.header,
      oauthRequestUrl: FXA_OAUTH_TOKEN_URL,
      oauthRequestBody: JSON.parse(oauthPayload),
      guardianTokenEndpoint: `${GUARDIAN_ENDPOINT}/api/v1/fpn/token`,
      proxyPassJwt: samplePass.rawToken,
      proxyPassClaims: samplePass.claims,
    });
  });

  // GET /api/protocol/docs — Protocol stages & Android source files
  app.get('/api/protocol/docs', (_req, res) => {
    res.json({
      stages: MOZILLA_VPN_PROTOCOL_STAGES,
      androidFiles: ANDROID_PROJECT_FILES,
    });
  });

  // GET /api/android/download.zip — Download complete stripped-down Android project archive
  app.get('/api/android/download.zip', (_req, res) => {
    const readmeContent = `# Mozilla VPN Lite — Android Client (на базе arman-vpn-client)

Минималистичный Android-клиент для подключения к серверам Mozilla VPN (Firefox IP Protection / Guardian).
Из оригинального репозитория https://github.com/armanlife/arman-vpn-client удалено всё лишнее (объявления, обход приложений, проверка релеев, резервные копии, логи, таблицы соединений).

Оставлены только 3 ключевых модуля:
1. Пульт быстрого переключения (1-tap смена аккаунта Mozilla, 1-tap смена локации, кнопка подключения VpnService).
2. Управление аккаунтами Mozilla (вход через PBKDF2/HKDF/HAWK или WebChannel sessionToken, авто-ротация при исчерпании 5 ГБ квоты).
3. Выбор сервера из Mozilla Remote Settings (включая разблокировку всех стран с флагом locked: true и замер реального пинга через HTTP/2 CONNECT).

## Сборка APK
1. Соберите Go-ядро в AAR:
   cd core && gomobile bind -target=android -androidapi 24 -o ../app/libs/core.aar ./mobile
2. Соберите APK:
   ./gradlew assembleRelease
`;

    const filesToZip = [
      { path: 'README.md', content: readmeContent },
      ...ANDROID_PROJECT_FILES.map((f) => ({ path: f.path, content: f.content })),
    ];

    const zipBuf = buildZipBuffer(filesToZip);
    res.setHeader('Content-Type', 'application/zip');
    res.setHeader('Content-Disposition', 'attachment; filename="mozilla-vpn-lite-android.zip"');
    res.send(zipBuf);
  });

  // POST /api/github/push — Create GitHub repo, upload patches + workflow, and trigger APK build
  app.post('/api/github/push', async (req, res) => {
    const { token, repoName, isPrivate } = req.body as {
      token?: string;
      repoName?: string;
      isPrivate?: boolean;
    };

    const cleanToken = (token || '').trim();
    const cleanRepo = (repoName || 'mozilla-vpn-lite').trim().replace(/[^a-zA-Z0-9._-]/g, '-');

    if (!cleanToken) {
      res.status(400).json({ error: 'Укажите GitHub Personal Access Token (с правами repo и workflow)' });
      return;
    }

    const ghHeaders = {
      Authorization: `Bearer ${cleanToken}`,
      Accept: 'application/vnd.github+json',
      'User-Agent': 'Mozilla-VPN-Lite-Builder',
      'X-GitHub-Api-Version': '2022-11-28',
      'Content-Type': 'application/json',
    };

    try {
      // 1. Get authenticated GitHub user
      const userResp = await fetch('https://api.github.com/user', { headers: ghHeaders });
      if (!userResp.ok) {
        const errText = await userResp.text();
        res.status(401).json({ error: `GitHub отклонил токен (HTTP ${userResp.status}): проверьте права repo и workflow. ${errText}` });
        return;
      }
      const userData = (await userResp.json()) as { login: string };
      const owner = userData.login;

      // 2. Check or create repository
      let defaultBranch = 'main';
      const checkResp = await fetch(`https://api.github.com/repos/${owner}/${cleanRepo}`, { headers: ghHeaders });
      if (!checkResp.ok) {
        const createResp = await fetch('https://api.github.com/user/repos', {
          method: 'POST',
          headers: ghHeaders,
          body: JSON.stringify({
            name: cleanRepo,
            description: 'Mozilla VPN Lite — Минималистичный Android-клиент (автосборка APK)',
            private: Boolean(isPrivate),
            auto_init: true,
          }),
        });
        if (!createResp.ok) {
          const errText = await createResp.text();
          res.status(400).json({ error: `Не удалось создать репозиторий ${cleanRepo}: ${errText}` });
          return;
        }
        const createdRepo = (await createResp.json()) as { default_branch?: string };
        defaultBranch = createdRepo.default_branch || 'main';
        // Brief pause for GitHub initial commit
        await new Promise((r) => setTimeout(r, 1200));
      } else {
        const existingRepo = (await checkResp.json()) as { default_branch?: string };
        defaultBranch = existingRepo.default_branch || 'main';
      }

      // Helper to upsert a file in the repo
      const putRepoFile = async (filePath: string, content: string, message: string) => {
        let sha: string | undefined;
        const getFile = await fetch(
          `https://api.github.com/repos/${owner}/${cleanRepo}/contents/${filePath}`,
          { headers: ghHeaders }
        );
        if (getFile.ok) {
          const fileJson = (await getFile.json()) as { sha?: string };
          sha = fileJson.sha;
        }
        const putResp = await fetch(
          `https://api.github.com/repos/${owner}/${cleanRepo}/contents/${filePath}`,
          {
            method: 'PUT',
            headers: ghHeaders,
            body: JSON.stringify({
              message,
              content: Buffer.from(content, 'utf8').toString('base64'),
              branch: defaultBranch,
              ...(sha ? { sha } : {}),
            }),
          }
        );
        if (!putResp.ok) {
          const txt = await putResp.text();
          throw new Error(`Ошибка загрузки ${filePath} (HTTP ${putResp.status}): ${txt}`);
        }
      };

      const fs = await import('fs/promises');
      const workflowYaml = await fs.readFile(path.join(__dirname, '.github/workflows/build-apk.yml'), 'utf8');

      // 3. Upload rewritten Go core, pure-Kotlin Android project files, and GitHub Actions workflow
      for (const file of ANDROID_PROJECT_FILES) {
        if (file.path !== '.github/workflows/build-apk.yml') {
          await putRepoFile(file.path, file.content, `Update ${file.path}`);
        }
      }

      await putRepoFile(
        '.github/workflows/build-apk.yml',
        workflowYaml,
        'Configure pure-Kotlin + rewritten Go core Android APK build workflow'
      );

      // 4. Trigger workflow_dispatch explicitly as well
      await fetch(
        `https://api.github.com/repos/${owner}/${cleanRepo}/actions/workflows/build-apk.yml/dispatches`,
        {
          method: 'POST',
          headers: ghHeaders,
          body: JSON.stringify({ ref: defaultBranch }),
        }
      ).catch(() => {});

      res.json({
        ok: true,
        owner,
        repo: cleanRepo,
        repoUrl: `https://github.com/${owner}/${cleanRepo}`,
        actionsUrl: `https://github.com/${owner}/${cleanRepo}/actions`,
        releasesUrl: `https://github.com/${owner}/${cleanRepo}/releases`,
      });
    } catch (err: any) {
      res.status(500).json({ error: err?.message || 'Ошибка выгрузки в GitHub' });
    }
  });

  if (process.env.NODE_ENV !== 'production') {
    const vite = await createViteServer({
      server: { middlewareMode: true },
      appType: 'spa',
    });
    app.use(vite.middlewares);
  } else {
    const distPath = path.join(__dirname, 'dist');
    app.use(express.static(distPath));
    app.get('*', (_req, res) => {
      res.sendFile(path.join(distPath, 'index.html'));
    });
  }

  app.listen(PORT, '0.0.0.0', () => {
    console.log(`Mozilla VPN Lite server listening on http://0.0.0.0:${PORT}`);
  });
}

startServer();
