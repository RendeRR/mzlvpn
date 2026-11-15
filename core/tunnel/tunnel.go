// Package tunnel runs the local loopback SOCKS5 server (for Android Tun2Socks)
// and multiplexes outbound TCP and DNS traffic over an HTTP/2 TLS 1.3 CONNECT
// session to the selected Mozilla Fastly proxy (*.m1.fastly-masque.net:2499)
// using the active account's Guardian ProxyPass bearer JWT.
//
// Protocol notes (verified against hev-socks5-tunnel 2.19 sources):
//   - TCP: SOCKS5 CONNECT (cmd 0x01) -> HTTP/2 CONNECT, :authority = target
//     (sent via URL.Opaque = targetAuthority + Host = targetAuthority).
//   - UDP: hev is configured with udp: 'tcp', so datagrams arrive on a TCP
//     connection as SOCKS5 FWD_UDP (cmd 0x05) with per-datagram framing
//     [len u16][hdrlen u8][socks addr][payload]. Only DNS (port 53) can be
//     carried: it is resolved through the H2 tunnel — primarily via
//     DNS-over-HTTPS to Cloudflare on :443 (CONNECT proxies commonly refuse
//     egress to :53 while :443 is the port the data plane already uses),
//     with classic DNS-over-TCP to 1.1.1.1:53 as fallback. All other UDP
//     (QUIC etc.) is dropped — the transport has no UDP bearer.
//   - SOCKS5 UDP ASSOCIATE (cmd 0x03) is rejected with rep 0x07.
//   - A small pool of H2 connections (up to maxH2Conns) is kept so a busy
//     connection is never torn down just because its stream limit is hit.
//   - 32 KB buffer pool (sync.Pool + io.CopyBuffer) for stream relay.
//   - Live byte counting and X-Quota-* header inspection for quota stats.
package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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

type countWriter struct {
	w      io.Writer
	onSent func(n int64)
}

func (cw countWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 && cw.onSent != nil {
		cw.onSent(int64(n))
	}
	return n, err
}

func copyZeroAlloc(dst io.Writer, src io.Reader, onBytes func(n int64)) {
	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	if onBytes != nil {
		_, _ = io.CopyBuffer(countWriter{w: dst, onSent: onBytes}, src, *bufPtr)
	} else {
		_, _ = io.CopyBuffer(dst, src, *bufPtr)
	}
}

func configureLowLatencyTCP(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
}

// maxH2Conns bounds the pool of parallel HTTP/2 sessions to the proxy.
const maxH2Conns = 4

type h2ConnEntry struct {
	cc  *http2.ClientConn
	raw net.Conn
}

type Engine struct {
	mu             sync.RWMutex
	dialMu         sync.Mutex
	listener       net.Listener
	proxyHost      string
	bearerToken    string
	h2Pool         []h2ConnEntry
	tlsCfg         *tls.Config // test hook: overrides the default TLS config when set
	dohMu          sync.Mutex
	dohConn        *tls.Conn
	dohReader      *bufio.Reader
	OnTrafficBytes func(deltaBytes int64)
	OnQuotaHeaders func(h http.Header)
	OnQuotaExhaust func()
}

func NewEngine() *Engine {
	return &Engine{}
}

// Start begins listening for local SOCKS5 connections from Android VpnService (Tun2Socks).
func (e *Engine) Start(listenAddr, proxyEndpoint, proxyPassJWT string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.proxyHost != proxyEndpoint {
		e.closeH2Locked()
	}
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
	for _, entry := range e.h2Pool {
		if entry.raw != nil {
			_ = entry.raw.Close()
		}
	}
	e.h2Pool = nil
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
	// No-auth reply (loopback only)
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	var reqHdr [4]byte
	if _, err := io.ReadFull(client, reqHdr[:]); err != nil || reqHdr[0] != 0x05 {
		return
	}

	cmd := reqHdr[1]
	host, port, err := readSocksAddr(client, reqHdr[3])
	if err != nil {
		// Tell the client why instead of silently hanging up: hev marks the
		// session failed fast and apps do not stall on a dead request.
		writeSocksReply(client, 0x08) // address type not supported
		return
	}

	switch cmd {
	case 0x01: // Standard TCP CONNECT
		authority := net.JoinHostPort(host, strconv.Itoa(int(port)))
		// Open the upstream stream FIRST and only then report success:
		// a success reply followed by an instant EOF makes hev/apps treat
		// every site as reachable-but-broken and hides the real error.
		// The stream context is Background: an H2 stream must live as long
		// as the TCP connection it carries (only dialing is time-boxed,
		// inside openH2ConnectStream).
		streamReader, streamWriter, err := e.openH2ConnectStream(context.Background(), authority)
		if err != nil {
			writeSocksReply(client, 0x05) // connection refused
			return
		}
		defer streamWriter.Close()
		defer streamReader.Close()
		if err := writeSocksReply(client, 0x00); err != nil {
			return
		}
		_ = client.SetDeadline(time.Time{})
		e.relayTCPStream(client, streamReader, streamWriter)

	case 0x05: // hev-socks5-tunnel FWD_UDP (udp: 'tcp') — HevSocks5UDPHdr framing
		if err := writeSocksReply(client, 0x00); err != nil {
			return
		}
		_ = client.SetDeadline(time.Time{})
		e.handleHevUDPInTCP(client)

	default:
		// Includes UDP ASSOCIATE (0x03): not offered — hev runs with udp:'tcp'.
		writeSocksReply(client, 0x07) // command not supported
	}
}

// writeSocksReply sends a SOCKS5 reply with a zeroed IPv4 bind address.
func writeSocksReply(client net.Conn, rep byte) error {
	_, err := client.Write([]byte{0x05, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func readSocksAddr(r io.Reader, atyp byte) (string, uint16, error) {
	var host string
	switch atyp {
	case 0x01: // IPv4
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(ip[:]).String()
	case 0x03: // Domain
		var dlen [1]byte
		if _, err := io.ReadFull(r, dlen[:]); err != nil {
			return "", 0, err
		}
		domain := make([]byte, int(dlen[0]))
		if _, err := io.ReadFull(r, domain); err != nil {
			return "", 0, err
		}
		host = string(domain)
	case 0x04: // IPv6
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(ip[:]).String()
	default:
		return "", 0, errors.New("unsupported atyp")
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return "", 0, err
	}
	return host, binary.BigEndian.Uint16(portBuf[:]), nil
}

// relayTCPStream pumps bytes between the local SOCKS client and an already
// established H2 CONNECT stream in both directions.
func (e *Engine) relayTCPStream(client net.Conn, streamReader io.ReadCloser, streamWriter io.WriteCloser) {
	onBytes := e.OnTrafficBytes
	go func() {
		copyZeroAlloc(streamWriter, client, onBytes)
		_ = streamWriter.Close()
	}()
	copyZeroAlloc(client, streamReader, onBytes)
}

// handleHevUDPInTCP handles hev-socks5-tunnel's UDP-in-TCP framing for
// FWD_UDP (cmd 0x05): each datagram is [len u16 BE][hdrlen u8][socks addr]
// [payload], where hdrlen = 3 + len(socks addr). Verified against
// hev-socks5-udp.c (hev_socks5_udp_sendmmsg_tcp / recvmmsg_tcp).
//
// Only DNS (destination port 53) is forwarded — bridged to DNS-over-TCP via
// the H2 tunnel. Any other UDP has no bearer in this transport and is
// dropped, so QUIC falls back to TCP instead of corrupting DNS framing.
func (e *Engine) handleHevUDPInTCP(client net.Conn) {
	var writeMu sync.Mutex
	for {
		var hdr [3]byte
		if _, err := io.ReadFull(client, hdr[:]); err != nil {
			return
		}
		dataLen := int(binary.BigEndian.Uint16(hdr[0:2]))
		hdrLen := int(hdr[2])
		if hdrLen < 4 || dataLen <= 0 || dataLen > 65535 {
			return
		}

		addrBytes := make([]byte, hdrLen-3)
		if _, err := io.ReadFull(client, addrBytes); err != nil {
			return
		}
		payload := make([]byte, dataLen)
		if _, err := io.ReadFull(client, payload); err != nil {
			return
		}

		if e.OnTrafficBytes != nil {
			e.OnTrafficBytes(int64(dataLen))
		}

		dstHost, dstPort, err := parseSocksAddrBytes(addrBytes)
		if err != nil || dstPort != 53 {
			continue // not DNS: no UDP bearer, drop the datagram
		}

		go func(rawAddr, udpPayload []byte) {
			respPayload, err := e.forwardDNS(udpPayload)
			if err != nil || len(respPayload) == 0 {
				// Visible in logcat: a silently dropped DNS answer is
				// indistinguishable from "no internet" on the device.
				log.Printf("mzlvpn: DNS forward to %s:53 failed: %v", dstHost, err)
				return
			}
			if e.OnTrafficBytes != nil {
				e.OnTrafficBytes(int64(len(respPayload)))
			}
			pkt := make([]byte, 3+len(rawAddr)+len(respPayload))
			binary.BigEndian.PutUint16(pkt[0:2], uint16(len(respPayload)))
			pkt[2] = byte(3 + len(rawAddr))
			copy(pkt[3:], rawAddr)
			copy(pkt[3+len(rawAddr):], respPayload)

			writeMu.Lock()
			_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = client.Write(pkt)
			writeMu.Unlock()
		}(addrBytes, payload)
	}
}

// parseSocksAddrBytes parses a SOCKS5 address block (atyp + addr + port)
// from raw bytes, as used inside hev UDP-in-TCP datagram headers.
func parseSocksAddrBytes(b []byte) (string, uint16, error) {
	if len(b) < 1 {
		return "", 0, errors.New("short socks address")
	}
	switch b[0] {
	case 0x01: // IPv4: 1 + 4 + 2
		if len(b) < 7 {
			return "", 0, errors.New("short ipv4 socks address")
		}
		return net.IP(b[1:5]).String(), binary.BigEndian.Uint16(b[5:7]), nil
	case 0x04: // IPv6: 1 + 16 + 2
		if len(b) < 19 {
			return "", 0, errors.New("short ipv6 socks address")
		}
		return net.IP(b[1:17]).String(), binary.BigEndian.Uint16(b[17:19]), nil
	case 0x03: // Domain: 1 + 1 + len + 2
		if len(b) < 2 {
			return "", 0, errors.New("short domain socks address")
		}
		l := int(b[1])
		if len(b) < 2+l+2 {
			return "", 0, errors.New("short domain socks address")
		}
		return string(b[2 : 2+l]), binary.BigEndian.Uint16(b[2+l : 2+l+2]), nil
	default:
		return "", 0, errors.New("unsupported atyp")
	}
}

// forwardDNS resolves one raw DNS query through the H2 tunnel. The primary
// path is DNS-over-HTTPS on :443 — the port the data plane already uses for
// everything else; CONNECT proxies that refuse egress to :53 were observed
// to kill all browser traffic while IP-based apps (Telegram) kept working.
// Classic DNS-over-TCP to 1.1.1.1:53 remains as fallback.
func (e *Engine) forwardDNS(dnsQuery []byte) ([]byte, error) {
	resp, err := e.forwardDNSOverHTTPS(dnsQuery)
	if err == nil && len(resp) > 0 {
		return resp, nil
	}
	dohErr := err
	resp, err = e.forwardDNSOverTCP(dnsQuery)
	if err != nil {
		return nil, fmt.Errorf("doh: %v; dns-over-tcp: %w", dohErr, err)
	}
	return resp, nil
}

// streamConn adapts the separate read/write halves of an H2 CONNECT stream
// to net.Conn so crypto/tls can run over it.
type streamConn struct {
	io.Reader
	io.Writer
	rc io.Closer
	wc io.Closer
}

func (c *streamConn) Close() error {
	_ = c.rc.Close()
	return c.wc.Close()
}

type streamAddr string

func (a streamAddr) Network() string { return "tcp" }
func (a streamAddr) String() string  { return string(a) }

func (c *streamConn) LocalAddr() net.Addr                { return streamAddr("h2-stream") }
func (c *streamConn) RemoteAddr() net.Addr               { return streamAddr("h2-stream") }
func (c *streamConn) SetDeadline(time.Time) error        { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error    { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error   { return nil }

// forwardDNSOverHTTPS sends the raw DNS message as an RFC 8484
// application/dns-message POST to Cloudflare DoH, over TLS inside an H2
// CONNECT stream to 1.1.1.1:443. One TLS connection is pooled and reused
// (queries are serialized — DNS volume is low and correctness beats
// parallelism here); a failed exchange closes it and retries once on a
// fresh connection.
func (e *Engine) forwardDNSOverHTTPS(dnsQuery []byte) ([]byte, error) {
	e.dohMu.Lock()
	defer e.dohMu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if e.dohConn == nil {
			conn, err := e.dialDoH()
			if err != nil {
				return nil, err
			}
			e.dohConn = conn
			e.dohReader = bufio.NewReader(conn)
		}
		resp, err := e.dohRoundTrip(dnsQuery)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		e.closeDoHLocked()
	}
	return nil, lastErr
}

func (e *Engine) dialDoH() (*tls.Conn, error) {
	// The H2 stream must outlive this call (it backs the pooled DoH
	// connection), so it is opened with Background: openH2ConnectStream
	// binds the stream to the caller's context, and cancelling that
	// context would tear the stream down. Only the TLS handshake gets
	// its own short-lived context.
	rc, wc, err := e.openH2ConnectStream(context.Background(), "1.1.1.1:443")
	if err != nil {
		return nil, err
	}
	raw := &streamConn{Reader: rc, Writer: wc, rc: rc, wc: wc}
	cfg := &tls.Config{
		ServerName:         "cloudflare-dns.com",
		MinVersion:         tls.VersionTLS12,
		ClientSessionCache: sharedSessionCache,
	}
	if e.tlsCfg != nil {
		cfg = e.tlsCfg.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = "cloudflare-dns.com"
		}
	}
	tlsConn := tls.Client(raw, cfg)
	hsCtx, hsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hsCancel()
	if err := tlsConn.HandshakeContext(hsCtx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (e *Engine) dohRoundTrip(dnsQuery []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, "https://cloudflare-dns.com/dns-query", bytes.NewReader(dnsQuery))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	if err := req.Write(e.dohConn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(e.dohReader, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("doh status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
	if err != nil {
		return nil, err
	}
	// A DoH answer must echo the query's transaction ID; anything else means
	// the pooled connection desynchronized and must not be trusted.
	if len(body) < 2 || len(dnsQuery) < 2 || body[0] != dnsQuery[0] || body[1] != dnsQuery[1] {
		return nil, errors.New("doh response txid mismatch")
	}
	return body, nil
}

func (e *Engine) closeDoHLocked() {
	if e.dohConn != nil {
		_ = e.dohConn.Close()
		e.dohConn = nil
		e.dohReader = nil
	}
}

// forwardDNSOverTCP encapsulates a raw UDP DNS query in RFC 1035 2-byte length framing
// and sends it through the Fastly HTTP/2 CONNECT tunnel to Cloudflare DNS (1.1.1.1:53).
func (e *Engine) forwardDNSOverTCP(dnsQuery []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	rc, wc, err := e.openH2ConnectStream(ctx, "1.1.1.1:53")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	defer wc.Close()

	tcpFrame := make([]byte, 2+len(dnsQuery))
	binary.BigEndian.PutUint16(tcpFrame[0:2], uint16(len(dnsQuery)))
	copy(tcpFrame[2:], dnsQuery)

	if _, err := wc.Write(tcpFrame); err != nil {
		return nil, err
	}

	var respLenBuf [2]byte
	if _, err := io.ReadFull(rc, respLenBuf[:]); err != nil {
		return nil, err
	}
	respLen := int(binary.BigEndian.Uint16(respLenBuf[:]))
	if respLen <= 0 || respLen > 65535 {
		return nil, errors.New("invalid DNS response length")
	}
	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(rc, respBuf); err != nil {
		return nil, err
	}
	return respBuf, nil
}

// sharedSessionCache is created once: a fresh LRU cache per connection made
// TLS session resumption useless.
var sharedSessionCache = tls.NewLRUClientSessionCache(32)

func (e *Engine) pickH2ConnLocked() *http2.ClientConn {
	for _, entry := range e.h2Pool {
		if entry.cc != nil && entry.cc.CanTakeNewRequest() {
			return entry.cc
		}
	}
	return nil
}

func (e *Engine) ensureH2Session(ctx context.Context) (*http2.ClientConn, string, error) {
	e.mu.RLock()
	if cc := e.pickH2ConnLocked(); cc != nil {
		token := e.bearerToken
		e.mu.RUnlock()
		return cc, token, nil
	}
	e.mu.RUnlock()

	e.dialMu.Lock()
	defer e.dialMu.Unlock()

	// Prune connections the peer or the network already closed, so a pool
	// full of corpses is never mistaken for a pool full of busy sessions.
	e.mu.Lock()
	alive := e.h2Pool[:0]
	for _, entry := range e.h2Pool {
		if entry.cc != nil && entry.cc.State().Closed {
			if entry.raw != nil {
				_ = entry.raw.Close()
			}
			continue
		}
		alive = append(alive, entry)
	}
	e.h2Pool = alive
	e.mu.Unlock()

	e.mu.RLock()
	if cc := e.pickH2ConnLocked(); cc != nil {
		token := e.bearerToken
		e.mu.RUnlock()
		return cc, token, nil
	}
	poolFull := len(e.h2Pool) >= maxH2Conns
	var busyConn *http2.ClientConn
	if poolFull && len(e.h2Pool) > 0 {
		busyConn = e.h2Pool[0].cc
	}
	proxyHost := e.proxyHost
	token := e.bearerToken
	tlsOverride := e.tlsCfg
	e.mu.RUnlock()

	if busyConn != nil {
		// All pooled connections are at their stream limit. Hand back the
		// first one and let RoundTrip report the limit for this single
		// stream — never tear down a connection with live streams.
		return busyConn, token, nil
	}

	host, _, err := net.SplitHostPort(proxyHost)
	if err != nil {
		return nil, "", err
	}

	rawDialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	rawConn, err := rawDialer.DialContext(ctx, "tcp", proxyHost)
	if err != nil {
		return nil, "", err
	}
	configureLowLatencyTCP(rawConn)

	var tlsCfg *tls.Config
	if tlsOverride != nil {
		tlsCfg = tlsOverride.Clone()
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = host
		}
	} else {
		tlsCfg = &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS13,
			NextProtos:         []string{"h2"},
			ClientSessionCache: sharedSessionCache,
		}
	}
	tlsConn := tls.Client(rawConn, tlsCfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, "", err
	}

	tr := &http2.Transport{
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,
	}
	newCC, err := tr.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, "", err
	}

	e.mu.Lock()
	e.h2Pool = append(e.h2Pool, h2ConnEntry{cc: newCC, raw: tlsConn})
	token = e.bearerToken
	e.mu.Unlock()

	return newCC, token, nil
}

// openH2ConnectStream opens an HTTP/2 CONNECT tunnel stream to targetAuthority.
// Uses URL.Opaque = targetAuthority and Host = targetAuthority so golang.org/x/net/http2
// sends `:method = CONNECT` and `:authority = targetAuthority` (without :scheme/:path).
func (e *Engine) openH2ConnectStream(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, error) {
	// Time-box only session establishment (TCP dial + TLS handshake).
	// The request itself keeps the caller's context: cancelling it would
	// tear down the live stream.
	dialCtx, dialCancel := context.WithTimeout(ctx, 20*time.Second)
	cc, token, err := e.ensureH2Session(dialCtx)
	dialCancel()
	if err != nil {
		return nil, nil, err
	}

	pr, pw := io.Pipe()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetAuthority},
		Host:   targetAuthority,
		Header: make(http.Header),
		Body:   pr,
	}
	req = req.WithContext(ctx)
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := cc.RoundTrip(req)
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}

	if e.OnQuotaHeaders != nil && resp.Header != nil {
		e.OnQuotaHeaders(resp.Header)
	}
	if resp.StatusCode == http.StatusTooManyRequests && e.OnQuotaExhaust != nil {
		go e.OnQuotaExhaust()
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		_ = pw.Close()
		if len(snippet) > 0 {
			return nil, nil, fmt.Errorf("CONNECT %s rejected HTTP %d: %s", targetAuthority, resp.StatusCode, snippet)
		}
		return nil, nil, fmt.Errorf("CONNECT %s rejected HTTP %d", targetAuthority, resp.StatusCode)
	}
	return resp.Body, pw, nil
}

// ProbeLatency measures the latency to a Fastly proxy node as the total time
// from the start of the TCP dial until the TLS 1.3 ALPN h2 handshake completes
// on the same connection (which also verifies the node is live).
func ProbeLatency(ctx context.Context, proxyEndpoint, _ string) (time.Duration, error) {
	return ProbeTLSHandshake(ctx, proxyEndpoint)
}

// ProbeTLSHandshake returns the full dial+handshake duration, NOT the bare TCP
// connect RTT. On networks where a local bypass proxy terminates TCP (e.g.
// fakeDNS setups that hand out local addresses for the proxy hostnames), the
// TCP RTT only measures the hop to that local proxy and is identical for every
// node on earth. The TLS handshake, in contrast, runs end-to-end to the real
// Fastly node (certificate verified against the real hostname), so its total
// duration tracks the same path the tunnel itself will take — through the
// local proxy where one exists, direct where none does.
func ProbeTLSHandshake(ctx context.Context, proxyEndpoint string) (time.Duration, error) {
	host, _, err := net.SplitHostPort(proxyEndpoint)
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	dialer := &net.Dialer{Timeout: 4 * time.Second}
	start := time.Now()
	rawConn, err := dialer.DialContext(probeCtx, "tcp", proxyEndpoint)
	if err != nil {
		return 0, err
	}

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"},
	})
	if err := tlsConn.HandshakeContext(probeCtx); err != nil {
		_ = rawConn.Close()
		return 0, err
	}
	_ = tlsConn.Close()
	total := time.Since(start)
	if total < time.Millisecond {
		total = time.Millisecond
	}
	return total, nil
}
