export interface AndroidProjectFile {
  path: string;
  language: 'kotlin' | 'go' | 'xml' | 'gradle' | 'markdown';
  title: string;
  description: string;
  content: string;
}

export interface ProtocolStage {
  step: string;
  title: string;
  endpoint: string;
  method: string;
  summary: string;
  details: string[];
  codeSnippet: string;
}

export const MOZILLA_VPN_PROTOCOL_STAGES: ProtocolStage[] = [
  {
    step: '01',
    title: 'Растяжение ключа пароля (FxA Key Stretching)',
    endpoint: 'identity.mozilla.com/picl/v1/quickStretch',
    method: 'PBKDF2 + HKDF-SHA256',
    summary: 'Клиент никогда не отправляет пароль в открытом виде. Email и пароль проходят через 1000 раундов PBKDF2-HMAC-SHA256 и HKDF-SHA256 для получения 32-байтного authPW.',
    details: [
      'Salt для PBKDF2: "identity.mozilla.com/picl/v1/quickStretch:" + email (регистр email критичен, при errno 120 сервер возвращает точный регистр аккаунта)',
      'Результат quickStretch (32 байта) подаётся в HKDF-SHA256 с info="identity.mozilla.com/picl/v1/authPW" и salt=0x00',
      'Реализовано в переписанном модуле core/fxa/fxa.go (DeriveAuthPW + LoginWithPassword)'
    ],
    codeSnippet: `salt := []byte("identity.mozilla.com/picl/v1/quickStretch:" + email)
quickStretchedPW := pbkdf2.Key([]byte(password), salt, 1000, 32, sha256.New)
hkdfReader := hkdf.New(sha256.New, quickStretchedPW, []byte{0x00}, []byte("identity.mozilla.com/picl/v1/authPW"))`
  },
  {
    step: '02',
    title: 'Аутентификация Firefox Accounts (HAWK + PKCE S256)',
    endpoint: 'https://api.accounts.firefox.com/v1/oauth/authorization',
    method: 'HAWK HMAC-SHA256',
    summary: 'Получение 64-символьного hex sessionToken через прямой вход API либо WebChannel и выпуск OAuth-токенов с подписью HAWK.',
    details: [
      'Прямой API: POST /v1/account/login?keys=true с verificationMethod="email-2fa" и User-Agent Firefox 145',
      'Из sessionToken через HKDF-SHA256 (info="identity.mozilla.com/picl/v1/sessionToken") выводятся hawkID (32B) и hawkKey (32B) для подписи HAWK',
      'Реализовано в core/fxa/fxa.go (DeriveHawkCredentials, HawkHeader, ExchangeSessionForOAuth, RefreshAccessToken)'
    ],
    codeSnippet: `// HAWK MAC: HMAC-SHA256(hawkKey, "hawk.1.header\\n" + ts + "\\n" + nonce + "\\nPOST\\n/v1/oauth/authorization...")
const FirefoxClientID = "5882386c6d801776"
const OAuthScope      = "profile https://identity.mozilla.com/apps/vpn"`
  },
  {
    step: '03',
    title: 'Guardian ProxyPass JWT + упреждающий прогрев резервного аккаунта (0 мс переключение)',
    endpoint: 'https://vpn.mozilla.org/api/v1/fpn/token',
    method: 'GET Bearer + Pre-warm',
    summary: 'Активация подписки FPN (/api/v1/fpn/activate), выпуск JWT-пропуска ProxyPass и упреждающий прогрев следующего аккаунта за 5% до порога.',
    details: [
      'При HTTP 403 от Guardian модуль core/guardian/guardian.go автоматически вызывает POST https://vpn.mozilla.org/api/v1/fpn/activate',
      'Когда квота активного аккаунта достигает (Threshold - 5%), горутина PrewarmNextReady заранее обновляет OAuth и кэширует свежий ProxyPass JWT для следующего аккаунта в пуле',
      'В момент достижения порога 95% (или HTTP 429) переключение на резервный аккаунт происходит за 0 миллисекунд прямо из оперативной памяти'
    ],
    codeSnippet: `// Pre-warm standby account when active account crosses (Threshold - 5%):
if pass.Usage.UsedPercent() >= (threshold - 5.0) {
    go p.PrewarmNextReady(acc.ID)
}`
  },
  {
    step: '04',
    title: 'Zero-Copy HTTP/2 туннель (sync.Pool 32KB + TCP_NODELAY + NetworkCallback)',
    endpoint: 'https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records',
    method: 'TLS 1.3 ALPN h2 + sync.Pool',
    summary: 'Загрузка всех серверов (*.m1.fastly-masque.net:2499), пул 32 КБ буферов без нагрузки на GC и мгновенный реконнект при переходе Wi-Fi ↔ LTE.',
    details: [
      'В core/tunnel/tunnel.go используется sync.Pool фиксированных 32 КБ буферов (io.CopyBuffer) — нулевые аллокации памяти при передаче данных',
      'На всех сокетах включены TCP_NODELAY, KeepAlive 30s и TLS 1.3 ClientSessionCache для ускорения рукопожатий',
      'В MinimalVpnService.kt зарегистрирован ConnectivityManager.NetworkCallback, вызывающий mobile.Mobile.reconnect() в момент смены Wi-Fi <-> мобильная сеть без ожидания таймаута'
    ],
    codeSnippet: `var streamBufferPool = sync.Pool{
    New: func() any { b := make([]byte, 32*1024); return &b },
}
// + ConnectivityManager.NetworkCallback -> mobile.Mobile.reconnect()`
  }
];

export const ANDROID_PROJECT_FILES: AndroidProjectFile[] = [
  {
    path: 'core/tunnel/tunnel.go',
    language: 'go',
    title: 'core/tunnel/tunnel.go (Zero-Alloc sync.Pool + TCP_NODELAY + ResetSession)',
    description: 'Оптимизированный движок туннеля: пул 32 КБ буферов (sync.Pool + io.CopyBuffer) без аллокаций в куче, TCP_NODELAY, кэш сессий TLS 1.3 и мгновенный сброс ResetSession() при смене сети.',
    content: `// Package tunnel runs the local loopback SOCKS5 server (for Android Tun2Socks)
// and multiplexes outbound connections over an HTTP/2 TLS 1.3 CONNECT session
// to the selected Mozilla Fastly proxy (*.m1.fastly-masque.net:2499) using the
// active account's Guardian ProxyPass bearer JWT.
//
// Optimizations:
//   - Zero-allocation 32 KB buffer pool (sync.Pool + io.CopyBuffer) to eliminate GC pressure
//   - TCP_NODELAY and 30s KeepAlive on loopback and upstream Fastly sockets
//   - Instant ResetSession() for zero-wait Wi-Fi <-> Cellular network handoffs
package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"mozillavpnlite/core/fxa"
)

const copyBufSize = 32 * 1024

var streamBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufSize)
		return &b
	},
}

func copyZeroAlloc(dst io.Writer, src io.Reader) {
	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	_, _ = io.CopyBuffer(dst, src, *bufPtr)
}

func configureLowLatencyTCP(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
}

type Engine struct {
	mu          sync.RWMutex
	listener    net.Listener
	proxyHost   string
	bearerToken string
	h2Conn      *http2.ClientConn
	rawTLS      net.Conn
}

func NewEngine() *Engine {
	return &Engine{}
}

// Start begins listening for local SOCKS5 connections from Android VpnService (Tun2Socks).
func (e *Engine) Start(listenAddr, proxyEndpoint, proxyPassJWT string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.proxyHost = proxyEndpoint
	e.bearerToken = proxyPassJWT

	if e.listener == nil {
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return 0, err
		}
		e.listener = ln
		go e.acceptLoop(ln)
	}
	return e.listener.Addr().(*net.TCPAddr).Port, nil
}

// UpdateToken hot-swaps the active Mozilla account's ProxyPass JWT in 0 ms without dropping the tunnel.
func (e *Engine) UpdateToken(proxyPassJWT string) {
	e.mu.Lock()
	e.bearerToken = proxyPassJWT
	e.mu.Unlock()
}

// SwitchServer changes the upstream Mozilla Fastly endpoint and resets the pooled H2 connection.
func (e *Engine) SwitchServer(proxyEndpoint string) {
	e.mu.Lock()
	if e.proxyHost != proxyEndpoint {
		e.proxyHost = proxyEndpoint
		e.closeH2Locked()
	}
	e.mu.Unlock()
}

// ResetSession immediately drops the pooled TLS 1.3 / HTTP/2 session when Android
// switches network interfaces (e.g. Wi-Fi <-> LTE/5G) so the next packet dials immediately.
func (e *Engine) ResetSession() {
	e.mu.Lock()
	e.closeH2Locked()
	e.mu.Unlock()
}

func (e *Engine) closeH2Locked() {
	if e.rawTLS != nil {
		_ = e.rawTLS.Close()
		e.rawTLS = nil
		e.h2Conn = nil
	}
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listener != nil {
		_ = e.listener.Close()
		e.listener = nil
	}
	e.closeH2Locked()
}

func (e *Engine) Port() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.listener == nil {
		return 0
	}
	return e.listener.Addr().(*net.TCPAddr).Port
}

func (e *Engine) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		configureLowLatencyTCP(conn)
		go e.handleSocks5(conn)
	}
}

func (e *Engine) handleSocks5(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	var hdr [2]byte
	if _, err := io.ReadFull(client, hdr[:]); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	_, _ = client.Write([]byte{0x05, 0x00})

	var reqHdr [4]byte
	if _, err := io.ReadFull(client, reqHdr[:]); err != nil || reqHdr[0] != 0x05 || reqHdr[1] != 0x01 {
		return
	}

	var host string
	switch reqHdr[3] {
	case 0x01:
		var ip [4]byte
		if _, err := io.ReadFull(client, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03:
		var dlen [1]byte
		if _, err := io.ReadFull(client, dlen[:]); err != nil {
			return
		}
		domain := make([]byte, int(dlen[0]))
		if _, err := io.ReadFull(client, domain); err != nil {
			return
		}
		host = string(domain)
	case 0x04:
		var ip [16]byte
		if _, err := io.ReadFull(client, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	default:
		return
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(client, portBuf[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	authority := net.JoinHostPort(host, strconv.Itoa(int(port)))

	_, _ = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	_ = client.SetDeadline(time.Time{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamReader, streamWriter, err := e.openH2ConnectStream(ctx, authority)
	if err != nil {
		return
	}
	defer streamWriter.Close()
	defer streamReader.Close()

	go func() {
		copyZeroAlloc(streamWriter, client)
		_ = streamWriter.Close()
	}()
	copyZeroAlloc(client, streamReader)
}

func (e *Engine) ensureH2Session(ctx context.Context) (*http2.ClientConn, string, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.h2Conn != nil && e.h2Conn.CanTakeNewRequest() {
		return e.h2Conn, e.proxyHost, e.bearerToken, nil
	}

	host, _, err := net.SplitHostPort(e.proxyHost)
	if err != nil {
		return nil, "", "", err
	}

	rawDialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	rawConn, err := rawDialer.DialContext(ctx, "tcp", e.proxyHost)
	if err != nil {
		return nil, "", "", err
	}
	configureLowLatencyTCP(rawConn)

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"h2"},
		ClientSessionCache: tls.NewLRUClientSessionCache(32),
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, "", "", err
	}

	tr := &http2.Transport{
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,
	}
	cc, err := tr.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, "", "", err
	}

	e.rawTLS = tlsConn
	e.h2Conn = cc
	return cc, e.proxyHost, e.bearerToken, nil
}

func (e *Engine) openH2ConnectStream(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, error) {
	cc, proxyHost, token, err := e.ensureH2Session(ctx)
	if err != nil {
		return nil, nil, err
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+targetAuthority, pr)
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	req.Host = targetAuthority
	req.URL.Host = proxyHost
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := cc.RoundTrip(req)
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		_ = pw.Close()
		return nil, nil, errors.New(fmt.Sprintf("CONNECT rejected HTTP %d", resp.StatusCode))
	}
	return resp.Body, pw, nil
}

func ProbeLatency(ctx context.Context, proxyEndpoint, bearerToken string) (time.Duration, error) {
	eng := NewEngine()
	eng.proxyHost = proxyEndpoint
	eng.bearerToken = bearerToken
	defer eng.Stop()

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	if _, _, _, err := eng.ensureH2Session(probeCtx); err != nil {
		return 0, err
	}

	start := time.Now()
	rc, wc, err := eng.openH2ConnectStream(probeCtx, "www.google.com:443")
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	_ = wc.Close()
	_ = rc.Close()
	return elapsed, nil
}
`
  },
  {
    path: 'core/accounts/pool.go',
    language: 'go',
    title: 'core/accounts/pool.go (Упреждающий прогрев PrewarmNextReady и 0-мс ротация)',
    description: 'Пул аккаунтов Mozilla с фоновым предпрогревом следующего аккаунта при (Threshold - 5%) и мгновенным возвратом кэшированного токена ProxyPass за 0 мс.',
    content: `// Package accounts manages the pool of Mozilla accounts, credential
// persistence, instant 1-tap profile switching, background pre-warming of the
// standby account at (Threshold - 5%), and 0-ms automatic failover when an
// account's 5 GB monthly quota reaches the configured threshold.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/guardian"
)

type Status string

const (
	StatusReady          Status = "ready"
	StatusReserved       Status = "reserved"
	StatusReauthRequired Status = "reauth_required"
)

type Account struct {
	ID       string               \`json:"id"\`
	Email    string               \`json:"email"\`
	Position int                  \`json:"position"\`
	Active   bool                 \`json:"active"\`
	Enabled  bool                 \`json:"enabled"\`
	Status   Status               \`json:"status"\`
	Reason   string               \`json:"reason,omitempty"\`
	Tokens   fxa.OAuthTokens      \`json:"tokens"\`
	LastPass *guardian.ProxyPass  \`json:"last_pass,omitempty"\`
	Usage    *guardian.QuotaUsage \`json:"usage,omitempty"\`
}

func (a *Account) HasWarmPass() bool {
	return a != nil && a.LastPass != nil && a.LastPass.RawToken != "" &&
		time.Now().Before(a.LastPass.ExpiresAt.Add(-5*time.Minute))
}

type Pool struct {
	mu               sync.Mutex
	prewarming       bool
	filePath         string
	ThresholdPercent int        \`json:"threshold_percent"\`
	Accounts         []*Account \`json:"accounts"\`
}

func LoadOrCreatePool(dataDir string) (*Pool, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	fp := filepath.Join(dataDir, "accounts_pool.json")
	p := &Pool{
		filePath:         fp,
		ThresholdPercent: 95,
		Accounts:         []*Account{},
	}
	if data, err := os.ReadFile(fp); err == nil {
		_ = json.Unmarshal(data, p)
	}
	if p.ThresholdPercent < 50 || p.ThresholdPercent > 100 {
		p.ThresholdPercent = 95
	}
	p.recomputeLocked()
	return p, nil
}

func (p *Pool) saveLocked() error {
	p.recomputeLocked()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.filePath, data, 0600)
}

func (p *Pool) recomputeLocked() {
	for i, acc := range p.Accounts {
		acc.Position = i + 1
		if acc.Usage != nil && !acc.Usage.Unlimited && acc.Usage.LimitBytes > 0 {
			if acc.Usage.RemainingBytes <= 0 || acc.Usage.UsedPercent() >= float64(p.ThresholdPercent) {
				if len(p.Accounts) > 1 {
					acc.Status = StatusReserved
					acc.Reason = "Достигнут порог месячной квоты 5 ГБ"
				}
			} else if acc.Status == StatusReserved {
				acc.Status = StatusReady
				acc.Reason = ""
			}
		}
	}
}

func (p *Pool) AddWithSessionToken(ctx context.Context, email, sessionTokenHex string) (*Account, error) {
	tokens, err := fxa.ExchangeSessionForOAuth(ctx, sessionTokenHex)
	if err != nil {
		return nil, err
	}
	pass, err := guardian.FetchProxyPass(ctx, tokens.AccessToken)
	if err != nil {
		return nil, err
	}

	var idBytes [6]byte
	_, _ = rand.Read(idBytes[:])

	p.mu.Lock()
	defer p.mu.Unlock()

	acc := &Account{
		ID:       "acc-" + hex.EncodeToString(idBytes[:]),
		Email:    email,
		Active:   len(p.Accounts) == 0,
		Enabled:  true,
		Status:   StatusReady,
		Tokens:   *tokens,
		LastPass: pass,
		Usage:    pass.Usage,
	}
	p.Accounts = append(p.Accounts, acc)
	_ = p.saveLocked()
	return acc, nil
}

// Activate immediately makes the given account ID active (0 ms if pre-warmed).
func (p *Pool) Activate(ctx context.Context, id string) (*guardian.ProxyPass, error) {
	p.mu.Lock()
	var target *Account
	for _, a := range p.Accounts {
		if a.ID == id {
			target = a
			break
		}
	}
	if target == nil {
		p.mu.Unlock()
		return nil, errors.New("аккаунт не найден")
	}
	for _, a := range p.Accounts {
		a.Active = (a.ID == id)
	}
	target.Enabled = true
	warmPass := target.LastPass
	isWarm := target.HasWarmPass()
	_ = p.saveLocked()
	p.mu.Unlock()

	if isWarm {
		go func(acc *Account) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = p.EnsureFreshPass(bgCtx, acc)
		}(target)
		return warmPass, nil
	}

	return p.EnsureFreshPass(ctx, target)
}

func (p *Pool) EnsureFreshPass(ctx context.Context, acc *Account) (*guardian.ProxyPass, error) {
	if time.Now().After(acc.Tokens.ExpiresAt.Add(-2 * time.Minute)) {
		refreshed, err := fxa.RefreshAccessToken(ctx, acc.Tokens.RefreshToken)
		if err != nil {
			p.mu.Lock()
			if errors.Is(err, fxa.ErrReauthRequired) {
				acc.Status = StatusReauthRequired
			}
			_ = p.saveLocked()
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Lock()
		acc.Tokens = *refreshed
		_ = p.saveLocked()
		p.mu.Unlock()
	}

	pass, err := guardian.FetchProxyPass(ctx, acc.Tokens.AccessToken)
	p.mu.Lock()
	threshold := float64(p.ThresholdPercent)
	if pass != nil && pass.Usage != nil {
		acc.Usage = pass.Usage
		acc.LastPass = pass
	}
	_ = p.saveLocked()
	p.mu.Unlock()

	if errors.Is(err, guardian.ErrQuotaExceeded) || (pass != nil && pass.Usage != nil && pass.Usage.UsedPercent() >= threshold) {
		return p.RotateToNextReady(ctx, acc.ID)
	}

	// Pre-warm standby account when active account crosses (Threshold - 5%)
	if pass != nil && pass.Usage != nil && pass.Usage.UsedPercent() >= (threshold-5.0) {
		go p.PrewarmNextReady(acc.ID)
	}

	return pass, err
}

// PrewarmNextReady refreshes OAuth and caches a fresh ProxyPass JWT on the next
// ready standby account in the background so failover happens in 0 ms.
func (p *Pool) PrewarmNextReady(activeID string) {
	p.mu.Lock()
	if p.prewarming {
		p.mu.Unlock()
		return
	}
	var standby *Account
	for _, a := range p.Accounts {
		if a.ID != activeID && a.Enabled && a.Status == StatusReady {
			standby = a
			break
		}
	}
	if standby == nil || standby.HasWarmPass() {
		p.mu.Unlock()
		return
	}
	p.prewarming = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.prewarming = false
		p.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_, _ = p.EnsureFreshPass(ctx, standby)
}

func (p *Pool) RotateToNextReady(ctx context.Context, skipID string) (*guardian.ProxyPass, error) {
	p.mu.Lock()
	var candidate *Account
	for _, a := range p.Accounts {
		if a.ID != skipID && a.Enabled && a.Status == StatusReady {
			candidate = a
			break
		}
	}
	if candidate == nil {
		p.mu.Unlock()
		return nil, errors.New("нет доступных резервных аккаунтов со свободной квотой")
	}
	for _, a := range p.Accounts {
		a.Active = (a.ID == candidate.ID)
	}
	warmPass := candidate.LastPass
	isWarm := candidate.HasWarmPass()
	_ = p.saveLocked()
	p.mu.Unlock()

	if isWarm {
		return warmPass, nil
	}
	return p.EnsureFreshPass(ctx, candidate)
}

func (p *Pool) ActiveAccount() *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.Accounts {
		if a.Active {
			return a
		}
	}
	if len(p.Accounts) > 0 {
		p.Accounts[0].Active = true
		return p.Accounts[0]
	}
	return nil
}
`
  },
  {
    path: 'core/mobile/mobile.go',
    language: 'go',
    title: 'core/mobile/mobile.go (Мост gomobile bind + Reconnect)',
    description: 'Точка входа core.aar: управление пулом аккаунтов, мгновенный Reconnect() при смене сети Wi-Fi <-> LTE, выбор локации и замер пинга.',
    content: `// Package mobile is the gomobile bind entry point for Android (libs/core.aar).
// Contains ONLY Mozilla account management, automatic 5 GB quota rotation,
// Remote Settings server selection, and the local SOCKS5 -> H2 CONNECT tunnel.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/accounts"
	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/serverlist"
	"mozillavpnlite/core/tunnel"
)

var (
	mu           sync.Mutex
	pool         *accounts.Pool
	engine       = tunnel.NewEngine()
	locations    []serverlist.Location
	selectedCode = "DE"
	connected    = false
)

func Start(dataDir string, autoConnect bool) error {
	mu.Lock()
	if pool != nil {
		mu.Unlock()
		if autoConnect {
			return Connect()
		}
		return nil
	}
	p, err := accounts.LoadOrCreatePool(dataDir)
	if err != nil {
		mu.Unlock()
		return err
	}
	pool = p
	mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if list, err := serverlist.Fetch(ctx); err == nil && len(list) > 0 {
			mu.Lock()
			locations = list
			mu.Unlock()
		}
	}()

	if autoConnect {
		return Connect()
	}
	return nil
}

func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return pool != nil
}

func Connect() error {
	mu.Lock()
	p := pool
	loc := currentLocationLocked()
	mu.Unlock()

	if p == nil {
		return errors.New("ядро не инициализировано")
	}
	active := p.ActiveAccount()
	if active == nil {
		return errors.New("добавьте хотя бы один аккаунт Mozilla")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pass, err := p.EnsureFreshPass(ctx, active)
	if err != nil {
		return err
	}

	_, err = engine.Start("127.0.0.1:0", loc.Endpoint(), pass.RawToken)
	if err != nil {
		return err
	}

	mu.Lock()
	connected = true
	mu.Unlock()
	return nil
}

// Reconnect immediately resets the HTTP/2 session when Android switches networks
// (e.g. Wi-Fi <-> LTE/5G) without waiting for old TCP timeouts.
func Reconnect() {
	engine.ResetSession()
	mu.Lock()
	p := pool
	isConn := connected
	mu.Unlock()
	if !isConn || p == nil {
		return
	}
	go func() {
		if active := p.ActiveAccount(); active != nil && !active.HasWarmPass() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if pass, err := p.EnsureFreshPass(ctx, active); err == nil && pass != nil {
				engine.UpdateToken(pass.RawToken)
			}
		}
	}()
}

func Disconnect() {
	engine.Stop()
	mu.Lock()
	connected = false
	mu.Unlock()
}

func SwitchAccount(accountID string) error {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return errors.New("ядро не инициализировано")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pass, err := p.Activate(ctx, accountID)
	if err != nil {
		return err
	}
	engine.UpdateToken(pass.RawToken)
	return nil
}

func AddAccountPassword(email, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionToken, canonicalEmail, err := fxa.LoginWithPassword(ctx, email, password)
	if err != nil {
		return err
	}
	return AddAccountSessionToken(canonicalEmail, sessionToken)
}

func AddAccountSessionToken(email, sessionTokenHex string) error {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return errors.New("ядро не инициализировано")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := p.AddWithSessionToken(ctx, email, sessionTokenHex)
	return err
}

func SetLocation(countryCode string) {
	mu.Lock()
	selectedCode = strings.ToUpper(strings.TrimSpace(countryCode))
	loc := currentLocationLocked()
	mu.Unlock()

	engine.SwitchServer(loc.Endpoint())
}

func PingAllLocations() error {
	mu.Lock()
	p := pool
	list := append([]serverlist.Location(nil), locations...)
	mu.Unlock()

	if p == nil {
		return errors.New("ядро не инициализировано")
	}
	active := p.ActiveAccount()
	if active == nil {
		return errors.New("для замера пинга нужен аккаунт Mozilla")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pass, err := p.EnsureFreshPass(ctx, active)
	if err != nil {
		return err
	}

	measured := serverlist.MeasureLatencies(ctx, list, func(c context.Context, endpoint string) (time.Duration, error) {
		return tunnel.ProbeLatency(c, endpoint, pass.RawToken)
	})

	mu.Lock()
	locations = measured
	mu.Unlock()
	return nil
}

func SocksPort() int {
	return engine.Port()
}

func StatusJSON() string {
	mu.Lock()
	defer mu.Unlock()

	var accs []*accounts.Account
	threshold := 95
	if pool != nil {
		accs = pool.Accounts
		threshold = pool.ThresholdPercent
	}

	payload := map[string]any{
		"connected":         connected,
		"socks_port":        engine.Port(),
		"selected_location": selectedCode,
		"location":          currentLocationLocked(),
		"threshold_percent": threshold,
		"accounts":          accs,
		"locations":         locations,
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func currentLocationLocked() serverlist.Location {
	for _, l := range locations {
		if strings.EqualFold(l.Code, selectedCode) {
			return l
		}
	}
	if len(locations) > 0 {
		return locations[0]
	}
	return serverlist.Location{
		Code:       "DE",
		Name:       "Germany",
		City:       "Frankfurt (FRA)",
		ServerHost: "fra104.m1.fastly-masque.net",
		ServerPort: 2499,
	}
}
`
  },
  {
    path: 'core/fxa/fxa.go',
    language: 'go',
    title: 'core/fxa/fxa.go (Криптография Firefox Accounts: PBKDF2 + HKDF + HAWK + PKCE)',
    description: 'Чистая реализация протокола авторизации Mozilla FxA: растяжение пароля, подпись HAWK HMAC-SHA256, обработка errno 120, обмен sessionToken на OAuth-токены и автообновление access_token.',
    content: `// Package fxa implements Mozilla Firefox Accounts authentication:
// PBKDF2 + HKDF password stretching, HAWK request signing, session login,
// PKCE OAuth code exchange, and access token refresh.
package fxa

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	AuthServer       = "https://api.accounts.firefox.com/v1"
	OAuthTokenURL    = "https://oauth.accounts.firefox.com/v1/token"
	FirefoxClientID  = "5882386c6d801776"
	OAuthScope       = "profile https://identity.mozilla.com/apps/vpn"
	FirefoxUserAgent = "Mozilla/5.0 (Android 14; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0"

	protocolVersion          = "identity.mozilla.com/picl/v1/"
	pbkdf2Rounds             = 1000
	stretchedPWLen           = 32
	hkdfLen                  = 32
	errnoIncorrectEmailCase  = 120
)

var ErrReauthRequired = errors.New("требуется повторный вход в аккаунт Mozilla")

type OAuthTokens struct {
	AccessToken  string    \`json:"access_token"\`
	RefreshToken string    \`json:"refresh_token"\`
	ExpiresIn    int       \`json:"expires_in"\`
	ExpiresAt    time.Time \`json:"expires_at"\`
}

func DeriveAuthPW(email, password string) ([]byte, error) {
	salt := []byte(protocolVersion + "quickStretch:" + email)
	quickStretched := pbkdf2.Key([]byte(password), salt, pbkdf2Rounds, stretchedPWLen, sha256.New)

	hkdfReader := hkdf.New(sha256.New, quickStretched, []byte{0x00}, []byte(protocolVersion+"authPW"))
	authPW := make([]byte, hkdfLen)
	if _, err := io.ReadFull(hkdfReader, authPW); err != nil {
		return nil, err
	}
	return authPW, nil
}

func DeriveHawkCredentials(tokenHex, contextName string) (id string, key []byte, err error) {
	tokenBytes, err := hex.DecodeString(strings.TrimSpace(tokenHex))
	if err != nil {
		return "", nil, fmt.Errorf("invalid token hex: %w", err)
	}
	info := []byte(protocolVersion + contextName)
	hkdfReader := hkdf.New(sha256.New, tokenBytes, nil, info)
	out := make([]byte, 96)
	if _, err := io.ReadFull(hkdfReader, out); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(out[:32]), out[32:64], nil
}

func HawkHeader(method, rawURL, hawkID string, hawkKey []byte, payload string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceStr := hex.EncodeToString(nonce)
	ts := fmt.Sprintf("%d", time.Now().Unix())

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	var payloadHash string
	if payload != "" {
		h := sha256.New()
		h.Write([]byte("hawk.1.payload\\napplication/json\\n"))
		h.Write([]byte(payload))
		h.Write([]byte("\\n"))
		payloadHash = hex.EncodeToString(h.Sum(nil))
	}

	normalized := strings.Join([]string{
		"hawk.1.header",
		ts,
		nonceStr,
		strings.ToUpper(method),
		u.RequestURI(),
		u.Hostname(),
		port,
		payloadHash,
		"",
		"",
	}, "\\n")

	mac := hmac.New(sha256.New, hawkKey)
	mac.Write([]byte(normalized))
	macStr := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf(\`Hawk id="%s", ts="%s", nonce="%s", mac="%s"\`, hawkID, ts, nonceStr, macStr)
	if payloadHash != "" {
		header += fmt.Sprintf(\`, hash="%s"\`, payloadHash)
	}
	return header, nil
}

func LoginWithPassword(ctx context.Context, email, password string) (sessionToken string, canonicalEmail string, err error) {
	email = strings.TrimSpace(strings.ToLower(email))
	tok, corrEmail, err := loginAttempt(ctx, email, "", password)
	if err != nil && corrEmail != "" && corrEmail != email {
		return loginAttempt(ctx, corrEmail, email, password)
	}
	return tok, email, err
}

func loginAttempt(ctx context.Context, stretchEmail, originalEmail, password string) (string, string, error) {
	authPW, err := DeriveAuthPW(stretchEmail, password)
	if err != nil {
		return "", "", err
	}
	bodyMap := map[string]any{
		"email":              stretchEmail,
		"authPW":             hex.EncodeToString(authPW),
		"reason":             "login",
		"service":            FirefoxClientID,
		"verificationMethod": "email-2fa",
	}
	if originalEmail != "" {
		bodyMap["originalLoginEmail"] = originalEmail
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthServer+"/account/login?keys=true", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Errno   int    \`json:"errno"\`
			Email   string \`json:"email"\`
			Message string \`json:"message"\`
		}
		_ = json.Unmarshal(data, &apiErr)
		if apiErr.Errno == errnoIncorrectEmailCase && apiErr.Email != "" {
			return "", apiErr.Email, fmt.Errorf("email case mismatch")
		}
		return "", "", fmt.Errorf("FxA login HTTP %d (errno %d): %s", resp.StatusCode, apiErr.Errno, apiErr.Message)
	}

	var loginResp struct {
		SessionToken string \`json:"sessionToken"\`
	}
	if err := json.Unmarshal(data, &loginResp); err != nil || loginResp.SessionToken == "" {
		return "", "", errors.New("пустой sessionToken в ответе FxA")
	}
	return loginResp.SessionToken, stretchEmail, nil
}

func ExchangeSessionForOAuth(ctx context.Context, sessionTokenHex string) (*OAuthTokens, error) {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return nil, err
	}

	var verifierBytes [32]byte
	if _, err := rand.Read(verifierBytes[:]); err != nil {
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes[:])
	challengeSum := sha256.Sum256([]byte(verifier))
	stateBytes := make([]byte, 12)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	authURL := AuthServer + "/oauth/authorization"
	authBody, _ := json.Marshal(map[string]string{
		"client_id":             FirefoxClientID,
		"response_type":         "code",
		"scope":                 OAuthScope,
		"state":                 state,
		"access_type":           "offline",
		"code_challenge":        base64.RawURLEncoding.EncodeToString(challengeSum[:]),
		"code_challenge_method": "S256",
	})

	authHeader, err := HawkHeader(http.MethodPost, authURL, hawkID, hawkKey, string(authBody))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(authBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth authorization HTTP %d", resp.StatusCode)
	}

	var codeResp struct {
		Code string \`json:"code"\`
	}
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil || codeResp.Code == "" {
		return nil, errors.New("не получен код авторизации OAuth")
	}

	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     FirefoxClientID,
		"grant_type":    "authorization_code",
		"code":          codeResp.Code,
		"code_verifier": verifier,
	})
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, bytes.NewReader(tokenBody))
	if err != nil {
		return nil, err
	}
	tokReq.Header.Set("Content-Type", "application/json")
	tokReq.Header.Set("User-Agent", FirefoxUserAgent)

	tokResp, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		return nil, err
	}
	defer tokResp.Body.Close()

	if tokResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth token exchange HTTP %d", tokResp.StatusCode)
	}

	var out OAuthTokens
	if err := json.NewDecoder(tokResp.Body).Decode(&out); err != nil {
		return nil, err
	}
	out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return &out, nil
}

func RefreshAccessToken(ctx context.Context, refreshToken string) (*OAuthTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     FirefoxClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         OAuthScope,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrReauthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth refresh HTTP %d", resp.StatusCode)
	}

	var out OAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return &out, nil
}
`
  },
  {
    path: 'core/guardian/guardian.go',
    language: 'go',
    title: 'core/guardian/guardian.go (Клиент Guardian API и парсер X-Quota-*)',
    description: 'Получение JWT-пропуска ProxyPass от https://vpn.mozilla.org/api/v1/fpn/token, автоматическая бесплатная активация при HTTP 403 и разбор заголовков квоты 5 ГБ.',
    content: `// Package guardian issues ProxyPass bearer JWTs from Mozilla's Guardian API
// (https://vpn.mozilla.org), activates free FPN entitlements on HTTP 403,
// and parses X-Quota-* monthly traffic headers.
package guardian

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mozillavpnlite/core/fxa"
)

const Endpoint = "https://vpn.mozilla.org"

var (
	ErrTokenInvalid  = errors.New("OAuth токен отклонён Guardian (HTTP 401)")
	ErrNotEntitled   = errors.New("аккаунт ещё не активирован в Guardian (HTTP 403)")
	ErrQuotaExceeded = errors.New("месячная квота 5 ГБ исчерпана (HTTP 429)")
)

type QuotaUsage struct {
	Unlimited      bool      \`json:"unlimited"\`
	LimitBytes     int64     \`json:"limit_bytes"\`
	RemainingBytes int64     \`json:"remaining_bytes"\`
	ResetAt        time.Time \`json:"reset_at"\`
}

func (u *QuotaUsage) UsedBytes() int64 {
	if u == nil || u.Unlimited || u.LimitBytes <= u.RemainingBytes {
		return 0
	}
	return u.LimitBytes - u.RemainingBytes
}

func (u *QuotaUsage) UsedPercent() float64 {
	if u == nil || u.Unlimited || u.LimitBytes <= 0 {
		return 0
	}
	return float64(u.UsedBytes()) * 100.0 / float64(u.LimitBytes)
}

type ProxyPass struct {
	RawToken  string      \`json:"raw_token"\`
	ExpiresAt time.Time   \`json:"expires_at"\`
	Usage     *QuotaUsage \`json:"usage,omitempty"\`
}

func FetchProxyPass(ctx context.Context, accessToken string) (*ProxyPass, error) {
	pass, err := fetchTokenOnce(ctx, accessToken)
	if errors.Is(err, ErrNotEntitled) {
		if actErr := Activate(ctx, accessToken); actErr == nil {
			return fetchTokenOnce(ctx, accessToken)
		}
	}
	return pass, err
}

func fetchTokenOnce(ctx context.Context, accessToken string) (*ProxyPass, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint+"/api/v1/fpn/token", nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	usage := parseQuotaHeaders(resp.Header)

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrTokenInvalid
	case http.StatusForbidden:
		return nil, ErrNotEntitled
	case http.StatusTooManyRequests:
		return &ProxyPass{Usage: usage}, ErrQuotaExceeded
	default:
		return nil, fmt.Errorf("Guardian HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return nil, err
	}
	var tokenResp struct {
		Token string \`json:"token"\`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.Token == "" {
		return nil, errors.New("пустой токен ProxyPass в ответе Guardian")
	}

	exp := parseJWTExpiry(tokenResp.Token)
	return &ProxyPass{
		RawToken:  tokenResp.Token,
		ExpiresAt: exp,
		Usage:     usage,
	}, nil
}

func Activate(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint+"/api/v1/fpn/activate", nil)
	if err != nil {
		return err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Guardian activate HTTP %d", resp.StatusCode)
	}
	return nil
}

func applyHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)
}

func parseQuotaHeaders(h http.Header) *QuotaUsage {
	if strings.EqualFold(strings.TrimSpace(h.Get("X-Quota-Unlimited")), "true") {
		return &QuotaUsage{Unlimited: true}
	}
	limit, err1 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Limit")), 10, 64)
	rem, err2 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Remaining")), 10, 64)
	if err1 != nil || err2 != nil || limit <= 0 {
		return nil
	}
	var resetAt time.Time
	rawReset := strings.TrimSpace(h.Get("X-Quota-Reset"))
	if t, err := time.Parse(time.RFC3339Nano, rawReset); err == nil {
		resetAt = t
	} else if sec, err := strconv.ParseInt(rawReset, 10, 64); err == nil && sec > 0 {
		resetAt = time.Unix(sec, 0)
	}
	return &QuotaUsage{
		LimitBytes:     limit,
		RemainingBytes: rem,
		ResetAt:        resetAt,
	}
}

func parseJWTExpiry(rawJWT string) time.Time {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return time.Now().Add(6 * time.Hour)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Now().Add(6 * time.Hour)
	}
	var claims struct {
		Exp int64 \`json:"exp"\`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp <= 0 {
		return time.Now().Add(6 * time.Hour)
	}
	return time.Unix(claims.Exp, 0)
}
`
  },
  {
    path: 'core/serverlist/serverlist.go',
    language: 'go',
    title: 'core/serverlist/serverlist.go (Загрузка серверов Remote Settings и замер пинга)',
    description: 'Загружает коллекцию vpn-serverlist из Mozilla Remote Settings, открывает доступ ко всем locked-странам и параллельно измеряет задержку до выходных узлов.',
    content: `// Package serverlist fetches Mozilla's Remote Settings vpn-serverlist collection,
// unlocks all locations (ignoring Firefox's locked flag), and measures real
// tunnel latency over HTTP/2 CONNECT.
package serverlist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
)

const RemoteSettingsURL = "https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records"

type Location struct {
	Code       string \`json:"code"\`
	Name       string \`json:"name"\`
	City       string \`json:"city"\`
	ServerHost string \`json:"server_host"\`
	ServerPort int    \`json:"server_port"\`
	Locked     bool   \`json:"locked"\`
	PingMS     int64  \`json:"ping_ms"\`
}

func (l Location) Endpoint() string {
	port := l.ServerPort
	if port <= 0 {
		port = 443
	}
	return fmt.Sprintf("%s:%d", l.ServerHost, port)
}

type rsRecord struct {
	Code   string \`json:"code"\`
	Name   string \`json:"name"\`
	Locked bool   \`json:"locked"\`
	Cities []struct {
		Name    string \`json:"name"\`
		Code    string \`json:"code"\`
		Servers []struct {
			Hostname    string \`json:"hostname"\`
			Port        int    \`json:"port"\`
			Quarantined bool   \`json:"quarantined"\`
		} \`json:"servers"\`
	} \`json:"cities"\`
}

func Fetch(ctx context.Context) ([]Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RemoteSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serverlist HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Data []rsRecord \`json:"data"\`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	var out []Location
	seen := make(map[string]bool)
	for _, rec := range envelope.Data {
		code := strings.ToUpper(strings.TrimSpace(rec.Code))
		if code == "" || code == "REC" || seen[code] {
			continue
		}
		for _, city := range rec.Cities {
			for _, srv := range city.Servers {
				if srv.Quarantined || srv.Hostname == "" {
					continue
				}
				port := srv.Port
				if port <= 0 {
					port = 2499
				}
				seen[code] = true
				out = append(out, Location{
					Code:       code,
					Name:       rec.Name,
					City:       fmt.Sprintf("%s (%s)", city.Name, city.Code),
					ServerHost: srv.Hostname,
					ServerPort: port,
					Locked:     rec.Locked,
				})
				break
			}
			if seen[code] {
				break
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func MeasureLatencies(ctx context.Context, locations []Location, dialProbe func(ctx context.Context, endpoint string) (time.Duration, error)) []Location {
	results := make([]Location, len(locations))
	copy(results, locations)

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			d, err := dialProbe(ctx, results[idx].Endpoint())
			if err == nil && d > 0 {
				results[idx].PingMS = d.Milliseconds()
			}
		}(i)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		aOk := results[i].PingMS > 0
		bOk := results[j].PingMS > 0
		if aOk != bOk {
			return aOk
		}
		if aOk && results[i].PingMS != results[j].PingMS {
			return results[i].PingMS < results[j].PingMS
		}
		return results[i].Name < results[j].Name
	})
	return results
}
`
  },
  {
    path: 'core/go.mod',
    language: 'go',
    title: 'core/go.mod (Зависимости переписанного Go-ядра)',
    description: 'Минимальный go.mod: только стандартная библиотека Go, golang.org/x/crypto (PBKDF2/HKDF) и golang.org/x/net (HTTP/2).',
    content: `module mozillavpnlite/core

go 1.23

require (
	golang.org/x/crypto v0.33.0
	golang.org/x/net v0.35.0
)
`
  },
  {
    path: 'android/app/src/main/kotlin/app/mozillavpnlite/MinimalVpnService.kt',
    language: 'kotlin',
    title: 'MinimalVpnService.kt (VpnService + NetworkCallback + Tun2Socks)',
    description: 'Системный сервис VpnService с ConnectivityManager.NetworkCallback: мгновенно вызывает mobile.Mobile.reconnect() при смене сети Wi-Fi <-> LTE/5G без ожидания TCP-таймаута.',
    content: `package app.mozillavpnlite

import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Network
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import java.io.File

class MinimalVpnService : VpnService() {

    private var connectivityManager: ConnectivityManager? = null
    private var lastNetworkHandle: Long = 0L

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            val handle = network.networkHandle
            if (lastNetworkHandle != 0L && lastNetworkHandle != handle && isRunning) {
                // Мгновенный сброс пула TLS 1.3 / HTTP/2 при переключении Wi-Fi <-> LTE/5G
                mobile.Mobile.reconnect()
            }
            lastNetworkHandle = handle
        }
    }

    override fun onCreate() {
        super.onCreate()
        connectivityManager = getSystemService(ConnectivityManager::class.java)
        try {
            connectivityManager?.registerDefaultNetworkCallback(networkCallback)
        } catch (_: Exception) {}
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            turnOff(this)
            stopSelf()
            return START_NOT_STICKY
        }
        startCore(this, on = true)
        establishTunnel()
        return START_STICKY
    }

    private fun establishTunnel() {
        val socksPort = mobile.Mobile.socksPort()
        val builder = Builder()
            .setSession("Mozilla VPN Lite")
            .setMtu(1500)
            .addAddress("10.0.0.2", 32)
            .addAddress("fd00::2", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("1.1.1.1")

        // Исключаем сам VPN-клиент, чтобы HTTP/2 CONNECT к Fastly шёл напрямую
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {}

        val tunFd: ParcelFileDescriptor = builder.establish() ?: return
        val configFile = File(cacheDir, "tproxy.yml").apply {
            writeText(
                """
                tunnel:
                  mtu: 1500
                  ipv4: 10.0.0.2
                  ipv6: 'fd00::2'
                socks5:
                  port: \$socksPort
                  address: 127.0.0.1
                  udp: 'udp'
                """.trimIndent()
            )
        }
        Tun2Socks.start(configFile.absolutePath, tunFd)
    }

    override fun onDestroy() {
        try {
            connectivityManager?.unregisterNetworkCallback(networkCallback)
        } catch (_: Exception) {}
        Tun2Socks.stop()
        super.onDestroy()
    }

    companion object {
        const val ACTION_STOP = "app.mozillavpnlite.STOP"

        @Volatile
        var isRunning = false
            private set

        fun dataDir(context: Context): File = File(context.filesDir, "core")

        @Synchronized
        fun startCore(context: Context, on: Boolean) {
            if (!mobile.Mobile.running()) {
                mobile.Mobile.start(dataDir(context).absolutePath, on)
            }
        }

        fun turnOn(context: Context) {
            val intent = Intent(context, MinimalVpnService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
            startCore(context, on = true)
            mobile.Mobile.connect()
            isRunning = true
        }

        fun turnOff(context: Context) {
            Tun2Socks.stop()
            if (mobile.Mobile.running()) {
                mobile.Mobile.disconnect()
            }
            isRunning = false
            context.stopService(Intent(context, MinimalVpnService::class.java))
        }
    }
}
`
  },
  {
    path: 'android/app/src/main/kotlin/app/mozillavpnlite/MainActivity.kt',
    language: 'kotlin',
    title: 'MainActivity.kt (Чистый нативный Jetpack Compose UI без Flutter)',
    description: 'Нативный Android-интерфейс на Jetpack Compose (~4.5 МБ APK без движка Flutter): Пульт, Аккаунты и Серверы.',
    content: `package app.mozillavpnlite

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        MinimalVpnService.startCore(this, on = false)

        setContent {
            MaterialTheme(
                colorScheme = darkColorScheme(
                    primary = Color(0xFF6366F1),
                    surface = Color(0xFF0F172A),
                    background = Color(0xFF020617)
                )
            ) {
                MinimalVpnApp(activity = this)
            }
        }
    }
}

@Composable
fun MinimalVpnApp(activity: ComponentActivity) {
    val scope = rememberCoroutineScope()
    var tab by remember { mutableIntStateOf(0) } // 0 = Пульт, 1 = Аккаунты, 2 = Серверы
    var state by remember { mutableStateOf(CoreBridge.fetchSnapshot()) }

    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        if (res.resultCode == Activity.RESULT_OK) {
            MinimalVpnService.turnOn(activity)
            state = CoreBridge.fetchSnapshot()
        }
    }

    LaunchedEffect(Unit) {
        while (true) {
            state = withContext(Dispatchers.IO) { CoreBridge.fetchSnapshot() }
            delay(1500)
        }
    }

    Scaffold(
        containerColor = Color(0xFF020617),
        bottomBar = {
            NavigationBar(containerColor = Color(0xFF0F172A)) {
                NavigationBarItem(
                    selected = tab == 0,
                    onClick = { tab = 0 },
                    icon = {},
                    label = { Text("Пульт", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 1,
                    onClick = { tab = 1 },
                    icon = {},
                    label = { Text("Аккаунты  (\${state.accounts.size})", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 2,
                    onClick = { tab = 2 },
                    icon = {},
                    label = { Text("Серверы  (\${state.locations.size})", fontWeight = FontWeight.SemiBold) }
                )
            }
        }
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when (tab) {
                0 -> QuickSwitchHome(
                    state = state,
                    onToggleVpn = {
                        if (state.connected) {
                            MinimalVpnService.turnOff(activity)
                        } else {
                            val prep: Intent? = VpnService.prepare(activity)
                            if (prep != null) vpnPermissionLauncher.launch(prep)
                            else MinimalVpnService.turnOn(activity)
                        }
                        state = CoreBridge.fetchSnapshot()
                    },
                    onSelectAccount = { id ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.switchAccount(id) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    },
                    onSelectLocation = { code ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.setLocation(code) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
                1 -> AccountsScreen(
                    state = state,
                    onActivate = { id ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.switchAccount(id) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    },
                    onAddAccount = { email, pass ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.addAccountPassword(email, pass) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
                2 -> ServersScreen(
                    state = state,
                    onSelect = { code ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.setLocation(code) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) {
                                state = next
                                tab = 0
                            }
                        }
                    },
                    onPingAll = {
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.pingAllLocations() } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
            }
        }
    }
}

@Composable
fun QuickSwitchHome(
    state: CoreSnapshot,
    onToggleVpn: () -> Unit,
    onSelectAccount: (String) -> Unit,
    onSelectLocation: (String) -> Unit
) {
    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(20.dp))
                    .background(if (state.connected) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .border(
                        1.dp,
                        if (state.connected) Color(0xFF6366F1) else Color(0xFF1E293B),
                        RoundedCornerShape(20.dp)
                    )
                    .clickable { onToggleVpn() }
                    .padding(20.dp)
            ) {
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        text = if (state.connected) "ЗАЩИЩЕНО · HTTP/2 TLS 1.3" else "ОТКЛЮЧЕНО",
                        color = if (state.connected) Color(0xFF34D399) else Color(0xFF94A3B8),
                        fontSize = 12.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = state.activeLocationName,
                        color = Color.White,
                        fontSize = 22.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = state.activeServerHost,
                        color = Color(0xFF94A3B8),
                        fontSize = 12.sp
                    )
                }
            }
        }

        items(state.accounts, key = { it.id }) { acc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(16.dp))
                    .background(if (acc.active) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .border(
                        1.dp,
                        if (acc.active) Color(0xFF6366F1) else Color(0xFF1E293B),
                        RoundedCornerShape(16.dp)
                    )
                    .clickable { onSelectAccount(acc.id) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Column {
                        Text(
                            text = "#\${acc.position}  \${acc.email}",
                            color = Color.White,
                            fontSize = 14.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                        Text(
                            text = "Осталось: \${acc.remainingGb} из 5.00 ГБ (\${acc.usedPercent}%)",
                            color = Color(0xFF94A3B8),
                            fontSize = 12.sp
                        )
                    }
                    Text(
                        text = if (acc.active) "Активен" else "Готов",
                        color = if (acc.active) Color(0xFF818CF8) else Color(0xFF64748B),
                        fontSize = 12.sp,
                        fontWeight = FontWeight.SemiBold
                    )
                }
            }
        }

        items(state.locations.take(6), key = { it.code }) { loc ->
            val selected = state.selectedLocation.equals(loc.code, ignoreCase = true)
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(14.dp))
                    .background(if (selected) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .clickable { onSelectLocation(loc.code) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Text(text = "\${loc.name} (\${loc.code})", color = Color.White, fontSize = 14.sp)
                    Text(
                        text = if (loc.pingMs > 0) "\${loc.pingMs} мс" else "—",
                        color = Color(0xFF94A3B8),
                        fontSize = 12.sp
                    )
                }
            }
        }
    }
}

@Composable
fun AccountsScreen(
    state: CoreSnapshot,
    onActivate: (String) -> Unit,
    onAddAccount: (String, String) -> Unit
) {
    var email by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }

    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = email,
                    onValueChange = { email = it },
                    label = { Text("Email аккаунта Mozilla") },
                    modifier = Modifier.fillMaxWidth()
                )
                OutlinedTextField(
                    value = password,
                    onValueChange = { password = it },
                    label = { Text("Пароль (PBKDF2 + HKDF)") },
                    modifier = Modifier.fillMaxWidth()
                )
                Button(
                    onClick = {
                        if (email.isNotBlank() && password.isNotBlank()) {
                            onAddAccount(email.trim(), password)
                            email = ""
                            password = ""
                        }
                    },
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text("Добавить аккаунт Mozilla")
                }
            }
        }

        items(state.accounts, key = { it.id }) { acc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(16.dp))
                    .background(Color(0xFF0F172A))
                    .clickable { onActivate(acc.id) }
                    .padding(14.dp)
            ) {
                Text(
                    text = "#\${acc.position} \${acc.email} · \${acc.remainingGb}",
                    color = Color.White,
                    fontSize = 14.sp
                )
            }
        }
    }
}

@Composable
fun ServersScreen(
    state: CoreSnapshot,
    onSelect: (String) -> Unit,
    onPingAll: () -> Unit
) {
    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        item {
            Button(onClick = onPingAll, modifier = Modifier.fillMaxWidth()) {
                Text("Измерить реальный пинг (HTTP/2 CONNECT)")
            }
        }
        items(state.locations, key = { it.code }) { loc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(14.dp))
                    .background(Color(0xFF0F172A))
                    .clickable { onSelect(loc.code) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Text(text = "\${loc.name} · \${loc.city}", color = Color.White, fontSize = 14.sp)
                    Text(
                        text = if (loc.pingMs > 0) "\${loc.pingMs} мс" else "—",
                        color = Color(0xFF34D399),
                        fontSize = 12.sp
                    )
                }
            }
        }
    }
}
`
  },
  {
    path: 'android/app/src/main/kotlin/app/mozillavpnlite/CoreBridge.kt',
    language: 'kotlin',
    title: 'CoreBridge.kt (Парсер состояния из Go core.aar)',
    description: 'Лёгкий мост между Jetpack Compose и Go-ядром (mobile.Mobile.statusJSON()) без сторонних сериализаторов.',
    content: `package app.mozillavpnlite

import org.json.JSONArray
import org.json.JSONObject

data class AccountItem(
    val id: String,
    val email: String,
    val position: Int,
    val active: Boolean,
    val status: String,
    val usedPercent: Int,
    val remainingGb: String
)

data class LocationItem(
    val code: String,
    val name: String,
    val city: String,
    val serverHost: String,
    val pingMs: Long
)

data class CoreSnapshot(
    val connected: Boolean = false,
    val selectedLocation: String = "DE",
    val activeLocationName: String = "Germany",
    val activeServerHost: String = "fra104.m1.fastly-masque.net:2499",
    val thresholdPercent: Int = 95,
    val accounts: List<AccountItem> = emptyList(),
    val locations: List<LocationItem> = emptyList()
)

object CoreBridge {
    fun fetchSnapshot(): CoreSnapshot {
        return try {
            val raw = mobile.Mobile.statusJSON()
            val root = JSONObject(raw)
            val locObj = root.optJSONObject("location")
            val accArr = root.optJSONArray("accounts") ?: JSONArray()
            val locArr = root.optJSONArray("locations") ?: JSONArray()

            val accounts = mutableListOf<AccountItem>()
            for (i in 0 until accArr.length()) {
                val o = accArr.optJSONObject(i) ?: continue
                val usage = o.optJSONObject("usage")
                val limit = usage?.optLong("limit_bytes", 5368709120L) ?: 5368709120L
                val rem = usage?.optLong("remaining_bytes", limit) ?: limit
                val usedPct = if (limit > 0) (((limit - rem) * 100L) / limit).toInt() else 0
                val remGb = String.format("%.2f ГБ", rem.toDouble() / (1024.0 * 1024.0 * 1024.0))
                accounts.add(
                    AccountItem(
                        id = o.optString("id"),
                        email = o.optString("email"),
                        position = o.optInt("position", i + 1),
                        active = o.optBoolean("active", false),
                        status = o.optString("status", "ready"),
                        usedPercent = usedPct,
                        remainingGb = remGb
                    )
                )
            }

            val locations = mutableListOf<LocationItem>()
            for (i in 0 until locArr.length()) {
                val o = locArr.optJSONObject(i) ?: continue
                locations.add(
                    LocationItem(
                        code = o.optString("code"),
                        name = o.optString("name"),
                        city = o.optString("city"),
                        serverHost = o.optString("server_host"),
                        pingMs = o.optLong("ping_ms", 0L)
                    )
                )
            }

            CoreSnapshot(
                connected = root.optBoolean("connected", false),
                selectedLocation = root.optString("selected_location", "DE"),
                activeLocationName = locObj?.optString("name", "Germany") ?: "Germany",
                activeServerHost = "\${locObj?.optString("server_host", "fra104.m1.fastly-masque.net")}:\${locObj?.optInt("server_port", 2499) ?: 2499}",
                thresholdPercent = root.optInt("threshold_percent", 95),
                accounts = accounts,
                locations = locations
            )
        } catch (_: Throwable) {
            CoreSnapshot()
        }
    }
}
`
  },
  {
    path: 'android/app/src/main/kotlin/app/mozillavpnlite/Tun2Socks.kt',
    language: 'kotlin',
    title: 'Tun2Socks.kt (Нативный JNI-мост к hev-socks5-tunnel)',
    description: 'Прямой вызов C-библиотеки libhev-socks5-tunnel.so для маршрутизации пакетов ядра Android из tun0 в локальный SOCKS5-сервер Go.',
    content: `package app.mozillavpnlite

import android.os.ParcelFileDescriptor

object Tun2Socks {
    private var activeTunFd: ParcelFileDescriptor? = null

    init {
        try {
            System.loadLibrary("hev-socks5-tunnel")
        } catch (_: Throwable) {}
    }

    @JvmStatic
    private external fun TProxyStartService(configPath: String, fd: Int)

    @JvmStatic
    private external fun TProxyStopService()

    @Synchronized
    fun start(configPath: String, tunFd: ParcelFileDescriptor) {
        stop()
        activeTunFd = tunFd
        try {
            TProxyStartService(configPath, tunFd.fd)
        } catch (_: Throwable) {}
    }

    @Synchronized
    fun stop() {
        val fd = activeTunFd ?: return
        activeTunFd = null
        try {
            TProxyStopService()
        } catch (_: Throwable) {}
        try {
            fd.close()
        } catch (_: Throwable) {}
    }
}
`
  },
  {
    path: 'android/app/build.gradle.kts',
    language: 'gradle',
    title: 'android/app/build.gradle.kts (Чистая сборка Kotlin + Compose с R8 минификацией)',
    description: 'Конфигурация сборки нативного Android-приложения с включённым R8/ShrinkResources (~4.5 МБ APK, без Flutter).',
    content: `plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "app.mozillavpnlite"
    compileSdk = 35
    ndkVersion = "27.0.12077973"

    defaultConfig {
        applicationId = "app.mozillavpnlite"
        minSdk = 24
        targetSdk = 35
        versionCode = 1
        versionName = "1.0.0-lite"

        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a")
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
    }
}

dependencies {
    implementation(files("libs/core.aar"))
    implementation(platform("androidx.compose:compose-bom:2024.10.01"))
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
}
`
  },
  {
    path: 'android/build.gradle.kts',
    language: 'gradle',
    title: 'android/build.gradle.kts (Корневой Gradle-файл)',
    description: 'Плагины Android Application и Kotlin Compose Compiler.',
    content: `plugins {
    id("com.android.application") version "8.7.2" apply false
    id("org.jetbrains.kotlin.android") version "2.0.21" apply false
    id("org.jetbrains.kotlin.plugin.compose") version "2.0.21" apply false
}
`
  },
  {
    path: 'android/settings.gradle.kts',
    language: 'gradle',
    title: 'android/settings.gradle.kts (Настройки Gradle-проекта)',
    description: 'Репозитории Google и MavenCentral для автономной сборки нативного Android-клиента.',
    content: `pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "MozillaVpnLite"
include(":app")
`
  },
  {
    path: 'android/gradle.properties',
    language: 'gradle',
    title: 'android/gradle.properties (Свойства JVM и AndroidX)',
    description: 'Оптимизация R-классов и памяти Gradle.',
    content: `org.gradle.jvmargs=-Xmx2048m -Dfile.encoding=UTF-8
android.useAndroidX=true
android.nonTransitiveRClass=true
kotlin.code.style=official
`
  },
  {
    path: 'android/app/src/main/AndroidManifest.xml',
    language: 'xml',
    title: 'android/app/src/main/AndroidManifest.xml (Минимальные разрешения)',
    description: 'Манифест Android-приложения: только INTERNET, ACCESS_NETWORK_STATE, FOREGROUND_SERVICE и BIND_VPN_SERVICE.',
    content: `<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android"
    package="app.mozillavpnlite">

    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
    <uses-permission android:name="android.permission.FOREGROUND_SERVICE" />
    <uses-permission android:name="android.permission.FOREGROUND_SERVICE_SPECIAL_USE" />

    <application
        android:label="Mozilla VPN Lite"
        android:allowBackup="false"
        android:supportsRtl="true"
        android:theme="@android:style/Theme.DeviceDefault.NoActionBar">

        <activity
            android:name=".MainActivity"
            android:exported="true"
            android:launchMode="singleTask">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>
        </activity>

        <service
            android:name=".MinimalVpnService"
            android:exported="false"
            android:permission="android.permission.BIND_VPN_SERVICE"
            android:foregroundServiceType="specialUse">
            <intent-filter>
                <action android:name="android.net.VpnService" />
            </intent-filter>
        </service>
    </application>
</manifest>
`
  },
  {
    path: '.github/workflows/build-apk.yml',
    language: 'markdown',
    title: '.github/workflows/build-apk.yml (Нативная сборка Kotlin + Compose + Go Core без Flutter)',
    description: 'Пайплайн GitHub Actions: компилирует переписанное Go-ядро в core.aar и собирает чистый нативный APK через Gradle (~4.5 МБ, без Flutter).',
    content: `name: Build Mozilla VPN Lite Android APK (Pure Kotlin + Compose + Rewritten Go Core)

on:
  push:
    branches: [ main, master ]
  workflow_dispatch:

permissions:
  contents: write

env:
  GOMOBILE_VERSION: v0.0.0-20260908204917-8b95e45f8d3e
  NDK_VERSION: 27.0.12077973

jobs:
  build-android-apk:
    runs-on: ubuntu-24.04
    steps:
      - name: Checkout Mozilla VPN Lite repository
        uses: actions/checkout@v4

      - name: Fetch C Tun2Socks (hev-socks5-tunnel) JNI sources
        run: |
          git clone --depth 1 https://github.com/armanlife/arman-vpn-client.git /tmp/upstream-jni
          mkdir -p android/app/src/main/cpp
          cp -r /tmp/upstream-jni/app/android/app/src/main/jni/* android/app/src/main/cpp/ 2>/dev/null || true

      - name: Set up Go 1.23
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Java 17
        uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: '17'

      - name: Setup Gradle
        uses: gradle/actions/setup-gradle@v4

      - name: Install Android NDK & gomobile
        run: |
          yes | "\$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" "ndk;\$NDK_VERSION" "platforms;android-35" "build-tools;35.0.0" > /dev/null
          echo "ANDROID_NDK_HOME=\$ANDROID_HOME/ndk/\$NDK_VERSION" >> "\$GITHUB_ENV"
          go install "golang.org/x/mobile/cmd/gomobile@\$GOMOBILE_VERSION" "golang.org/x/mobile/cmd/gobind@\$GOMOBILE_VERSION"

      - name: Compile Optimized Go Core (core.aar)
        run: |
          mkdir -p android/app/libs
          cd core
          go get "golang.org/x/mobile/bind@\$GOMOBILE_VERSION"
          go mod tidy
          gomobile bind -target=android -androidapi 24 -trimpath -ldflags "-s -w" \\
            -o ../android/app/libs/core.aar ./mobile

      - name: Build Pure Native Kotlin + Jetpack Compose Release APK (No Flutter)
        working-directory: android
        run: |
          gradle assembleRelease --no-daemon
          mkdir -p ../out
          cp app/build/outputs/apk/release/app-release.apk ../out/Mozilla-VPN-Lite-Native-Compose.apk

      - name: Upload Ready Android APK to Artifacts
        uses: actions/upload-artifact@v4
        with:
          name: Mozilla-VPN-Lite-Android-APK
          path: out/*.apk
          if-no-files-found: error

      - name: Create or Update Latest GitHub Release with APK
        env:
          GH_TOKEN: \${{ github.token }}
        run: |
          tag="v1.0.0-lite"
          gh release delete "\$tag" --yes --cleanup-tag 2>/dev/null || true
          gh release create "\$tag" out/*.apk \\
            --title "Mozilla VPN Lite (Pure Native Kotlin + Rewritten Go Core)" \\
            --notes "Чистая нативная сборка Kotlin + Jetpack Compose (~4.5 МБ, без Flutter) с переписанным и оптимизированным Go-ядром (sync.Pool 32KB, TCP_NODELAY, Pre-warm 2-го аккаунта и авто-реконнект Wi-Fi <-> LTE)."
`
  }
];
