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

export interface AppStateSnapshot {
  vpnConnected: boolean;
  connectedSince: string | null;
  selectedLocationMode: string;
  effectiveLocation: VpnLocation;
  tunnelProtocol: 'HTTP/3 MASQUE (QUIC)' | 'HTTP/2 CONNECT (TLS 1.3)';
  localSocksPort: number;
  tunInterface: string;
  thresholdPercent: number;
  lastSwitchEvent: string | null;
  accounts: MozillaAccount[];
  activeAccount: MozillaAccount | null;
  locations: VpnLocation[];
  serverListFetchedAt: string;
  serverListSource: 'live_mozilla_remote_settings' | 'cached_fallback';
}

export interface CryptoDerivationResult {
  email: string;
  pbkdf2Salt: string;
  quickStretchedHex: string;
  authPwHex: string;
  sessionTokenHex: string;
  hawkId: string;
  hawkKeyHex: string;
  hawkAuthorizationHeader: string;
  oauthRequestUrl: string;
  oauthRequestBody: Record<string, string>;
  guardianTokenEndpoint: string;
  proxyPassJwt: string;
  proxyPassClaims: Record<string, unknown>;
}
