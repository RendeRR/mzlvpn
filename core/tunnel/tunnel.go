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
	"strings"
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

func copyZeroAlloc(dst io.Writer, src io.Reader, onBytes func(n int64)) error {
	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	if onBytes != nil {
		_, err := io.CopyBuffer(countWriter{w: dst, onSent: onBytes}, src, *bufPtr)
		return err
	}
	_, err := io.CopyBuffer(dst, src, *bufPtr)
	return err
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
	tokenBroadcast chan struct{}
	h2Pool         []h2ConnEntry
	tlsCfg         *tls.Config // test hook: overrides the default TLS config when set
	dohMu          sync.Mutex
	dohConn        *tls.Conn
	dohReader      *bufio.Reader
	dohH2CC        *http2.ClientConn // multiplexed H2 client over dohConn when ALPN h2 is negotiated
	dohCC          *http2.ClientConn // upstream proxy session serving dohConn's stream
	watchStop      chan struct{}
	watchDone      chan struct{}
	tickMu         sync.Mutex
	lastTick       time.Time
	authMu         sync.Mutex
	lastAuthFail   time.Time
	deathMu        sync.Mutex
	preDataDeaths  map[*http2.ClientConn]int
	OnTrafficBytes func(deltaBytes int64)
	OnQuotaHeaders func(h http.Header)
	OnQuotaExhaust func()
	OnAuthFailure  func()
}

func NewEngine() *Engine {
	return &Engine{
		tokenBroadcast: make(chan struct{}),
	}
}

// Start begins listening for local SOCKS5 connections from Android VpnService (Tun2Socks).
func (e *Engine) Start(listenAddr, proxyEndpoint, proxyPassJWT string) (int, error) {
	// A start always begins from a clean transport slate. Stop() already
	// drops the pool, but the DoH connection and the package-level TLS
	// session cache used to survive a full service restart — a corpse in
	// either then outlived every recovery short of killing the process.
	e.dohMu.Lock()
	e.closeDoHLocked()
	e.dohCC = nil
	e.dohMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	fresh := e.listener == nil
	if e.proxyHost != proxyEndpoint || fresh {
		e.closeH2Locked()
	}
	if fresh {
		resetSessionCache()
	}
	e.proxyHost = proxyEndpoint
	e.bearerToken = proxyPassJWT
	if e.tokenBroadcast == nil {
		e.tokenBroadcast = make(chan struct{})
	}

	if e.listener == nil {
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return 0, err
		}
		e.listener = ln
		go e.acceptLoop(ln)
	}
	e.startWatchdogLocked()
	return e.listener.Addr().(*net.TCPAddr).Port, nil
}

const (
	// watchdogInterval is how often the suspension watchdog ticks.
	watchdogInterval = 5 * time.Second
	// suspensionGap is the tick-to-tick wall-clock jump that can only
	// mean the process was frozen (device asleep / Doze): normal
	// scheduling never delays a 5 s ticker by half a minute.
	suspensionGap = 30 * time.Second
)

// startWatchdogLocked starts the suspension watchdog if it is not
// running. Called with e.mu held (from Start).
func (e *Engine) startWatchdogLocked() {
	if e.watchStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	e.watchStop, e.watchDone = stop, done
	e.tickMu.Lock()
	e.lastTick = time.Now().Round(0)
	e.tickMu.Unlock()
	go e.watchdogLoop(stop, done)
}

func (e *Engine) watchdogLoop(stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			e.suspensionCheck(now)
		}
	}
}

// suspensionCheck reports whether the process was frozen since the
// previous tick. On wake from sleep the pooled sessions and the DoH
// connection all point at a path that silently died — NAT state and
// server-side sockets do not survive a long sleep — and no network
// callback fires when the device wakes on the same network, so nothing
// else would ever drop them. Detection resets the session.
func (e *Engine) suspensionCheck(now time.Time) bool {
	// The gap MUST be measured on the wall clock: Go's monotonic
	// reading (which Sub would prefer) rides CLOCK_MONOTONIC, and that
	// clock freezes while the system is suspended — a monotonic gap
	// across a night's sleep reads as ~0 and the suspension would never
	// be detected. Round(0) strips the monotonic reading from both
	// operands, so Sub compares wall time, which the RTC advances
	// through suspend. A user/NTP clock change can cause a false
	// positive; it costs one harmless session reset.
	now = now.Round(0)
	e.tickMu.Lock()
	if e.lastTick.IsZero() {
		e.lastTick = now
		e.tickMu.Unlock()
		return false
	}
	gap := now.Sub(e.lastTick)
	e.lastTick = now
	e.tickMu.Unlock()
	if gap > suspensionGap {
		e.ResetSession()
		return true
	}
	return false
}

// UpdateToken hot-swaps the active Mozilla account's ProxyPass JWT in 0 ms without dropping the tunnel
// and immediately wakes any concurrent streams waiting on a 401 refresh.
func (e *Engine) UpdateToken(proxyPassJWT string) {
	e.mu.Lock()
	changed := e.bearerToken != proxyPassJWT
	e.bearerToken = proxyPassJWT
	if changed {
		if e.tokenBroadcast != nil {
			close(e.tokenBroadcast)
		}
		e.tokenBroadcast = make(chan struct{})
	}
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

// isClosedConnErr reports whether err means "our own code closed this
// connection" — a stream or RoundTrip that died because an eviction or
// a pool flush closed the raw socket under it. Such errors are
// casualties of recovery, not evidence of a new failure: treating them
// as fresh evidence (evicting again, flushing the whole pool) turns a
// single dead session into a self-sustaining eviction storm that
// massacres every healthy stream in the pool — seen in the field as
// seconds of total breakage after a wake, with the log full of
// "use of closed network connection".
func isClosedConnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

// noteAuthFailure reports a 401 from the proxy: the bearer token the
// engine holds was rejected (expired during sleep, most likely — the
// token is only refreshed at Connect/rotation, and a pass can die
// while the device sleeps). The callback (mobile) fetches a fresh
// pass and hot-swaps it. Debounced: a burst of streams all meet the
// same dead token within the same second, and one refresh fixes all.
// Returns true whenever a refresh callback is now in flight (either
// triggered by this call or already triggered within the debounce window),
// so all concurrent streams hitting 401 wait for the same refresh.
func (e *Engine) noteAuthFailure() bool {
	e.authMu.Lock()
	cb := e.OnAuthFailure
	if cb == nil {
		e.authMu.Unlock()
		return false
	}
	if time.Since(e.lastAuthFail) < 10*time.Second {
		e.authMu.Unlock()
		return true
	}
	e.lastAuthFail = time.Now()
	e.authMu.Unlock()
	go cb()
	return true
}

// notePreDataDeath records a stream that died on cc before delivering
// a single byte, and reports whether the session has now earned
// eviction (two consecutive strikes). One pre-data death is weak
// evidence: the proxy sometimes sheds a single stream (target
// unreachable after the 200, stream-level load shedding) while the
// session itself is healthy — evicting on the first strike turned
// every post-unlock hiccup into a redial burst and a new egress IP
// per retry. A genuinely reaped session fails every stream, so the
// second strike lands within a second.
func (e *Engine) notePreDataDeath(cc *http2.ClientConn) bool {
	e.deathMu.Lock()
	defer e.deathMu.Unlock()
	if e.preDataDeaths == nil {
		e.preDataDeaths = make(map[*http2.ClientConn]int)
	}
	e.preDataDeaths[cc]++
	return e.preDataDeaths[cc] >= 2
}

// noteSessionAlive clears cc's pre-data death strikes: a stream on
// this session delivered data, so the session is demonstrably alive.
func (e *Engine) noteSessionAlive(cc *http2.ClientConn) {
	e.deathMu.Lock()
	delete(e.preDataDeaths, cc)
	e.deathMu.Unlock()
}

func (e *Engine) forgetSessionDeaths(cc *http2.ClientConn) {
	e.deathMu.Lock()
	delete(e.preDataDeaths, cc)
	e.deathMu.Unlock()
}

// rejectionError is an HTTP-level refusal from the proxy (a non-2xx
// CONNECT response): an authoritative answer, not a transport
// failure — the session that carried it is healthy and stays pooled.
type rejectionError struct {
	target string
	status int
	body   string
}

func (e *rejectionError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("CONNECT %s rejected HTTP %d: %s", e.target, e.status, e.body)
	}
	return fmt.Sprintf("CONNECT %s rejected HTTP %d", e.target, e.status)
}

// waitTokenChange blocks until the engine's bearer token is replaced
// (a refresh landing after a 401) or the timeout passes. Uses a broadcast
// channel closed by UpdateToken so all waiting streams resume in 0 ms.
func (e *Engine) waitTokenChange(timeout time.Duration, failedToken string) bool {
	e.mu.RLock()
	if e.bearerToken != failedToken {
		e.mu.RUnlock()
		return true
	}
	if e.tokenBroadcast == nil {
		e.mu.RUnlock()
		return false
	}
	waitCh := e.tokenBroadcast
	e.mu.RUnlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitCh:
		return true
	case <-timer.C:
		e.mu.RLock()
		changed := e.bearerToken != failedToken
		e.mu.RUnlock()
		return changed
	}
}

// Probe opens one CONNECT stream to the DoH authority and closes it,
// exercising the full data path (session dial + CONNECT) without user
// traffic. Run right after Connect: a dead-on-arrival path is healed by
// the failure handling it triggers and recorded in the diagnostic log.
func (e *Engine) Probe(ctx context.Context) error {
	rc, wc, err := e.openH2ConnectStream(ctx, "1.1.1.1:443")
	if err != nil {
		return err
	}
	_ = rc.Close()
	_ = wc.Close()
	return nil
}

// ResetSession immediately drops the pooled TLS 1.3 / HTTP/2 sessions and
// the pooled DoH connection when the path under them is gone — Android
// switched network interfaces (Wi-Fi <-> LTE/5G), or the device slept and
// woke on a silently-dead path — so the next packet dials immediately.
func (e *Engine) ResetSession() {
	e.deathMu.Lock()
	e.preDataDeaths = nil
	e.deathMu.Unlock()
	e.mu.Lock()
	e.closeH2Locked()
	e.mu.Unlock()
	e.dohMu.Lock()
	e.closeDoHLocked()
	e.dohMu.Unlock()
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
	stop, done := e.watchStop, e.watchDone
	e.watchStop, e.watchDone = nil, nil
	if e.listener != nil {
		_ = e.listener.Close()
		e.listener = nil
	}
	e.closeH2Locked()
	e.mu.Unlock()
	// Wait for the watchdog outside e.mu: it may be inside a
	// suspensionCheck -> ResetSession, which needs that lock.
	if stop != nil {
		close(stop)
		<-done
	}
	e.dohMu.Lock()
	e.closeDoHLocked()
	e.dohCC = nil
	e.dohMu.Unlock()
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
		streamReader, streamWriter, cc, err := e.openH2ConnectStreamCC(context.Background(), authority)
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
		e.relayTCPStream(client, cc, authority, streamReader, streamWriter)

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

// streamProbe records how an upstream stream ended: bytes delivered and
// the first read error. The relay needs both to tell a dead session from
// a stream that simply finished.
type streamProbe struct {
	r     io.Reader
	bytes int64
	err   error
}

func (p *streamProbe) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.bytes += int64(n)
	if err != nil && p.err == nil {
		p.err = err
	}
	return n, err
}

// relayTCPStream pumps bytes between the local SOCKS client and an already
// established H2 CONNECT stream in both directions.
//
// It is also the pool's mid-stream health check. A session whose path
// died (sleep, network switch) often still establishes streams — the
// proxy answers CONNECT — and only resets them mid-flight, which the
// browser reports as ERR_CONNECTION_RESET while the establishment path
// sees nothing wrong. So: if the upstream read fails before a single
// byte was delivered, or fails with a connection-level error at any
// point, the serving session is evicted; the next connection then dials
// a fresh one instead of re-entering the same corpse. A clean EOF after
// data, or a stream-scoped reset after data, leaves the session alone.
func (e *Engine) relayTCPStream(client net.Conn, cc *http2.ClientConn, authority string, streamReader io.ReadCloser, streamWriter io.WriteCloser) {
	onBytes := e.OnTrafficBytes
	go func() {
		_ = copyZeroAlloc(streamWriter, client, onBytes)
		_ = streamWriter.Close()
	}()
	probe := &streamProbe{r: streamReader}
	_ = copyZeroAlloc(client, probe, onBytes)
	if probe.err == nil || cc == nil {
		return
	}
	if isClosedConnErr(probe.err) {
		// This stream died because recovery closed its session under it.
		// It says nothing about any session's health — do not evict and
		// do not log it as an eviction; that feedback loop is the storm.
		return
	}
	if probe.bytes > 0 {
		e.noteSessionAlive(cc)
	}
	if probe.bytes == 0 {
		if e.notePreDataDeath(cc) {
			e.evictH2Conn(cc)
		} else {
		}
		return
	}
	if !errors.Is(probe.err, io.EOF) {
		var streamErr http2.StreamError
		if !errors.As(probe.err, &streamErr) {
			e.evictH2Conn(cc)
		}
	}
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

// synthesizeEmptyAAAA checks if dnsQuery is a standard single-question DNS query
// for QTYPE=28 (AAAA, IPv6) and QCLASS=1 (IN). Because the Android TUN interface
// is configured as IPv4-only (10.0.0.2/32 -> 0.0.0.0/0), returning an immediate
// RFC 1035 NOERROR empty response for AAAA queries avoids an unnecessary upstream
// DNS round-trip on every domain lookup and prevents apps from attempting direct
// IPv6 connections outside the tunnel.
func synthesizeEmptyAAAA(dnsQuery []byte) ([]byte, bool) {
	if len(dnsQuery) < 17 {
		return nil, false
	}
	flags := binary.BigEndian.Uint16(dnsQuery[2:4])
	// QR must be 0 (query) and Opcode must be 0 (standard query)
	if (flags&0x8000) != 0 || (flags&0x7800) != 0 {
		return nil, false
	}
	qdCount := binary.BigEndian.Uint16(dnsQuery[4:6])
	if qdCount != 1 {
		return nil, false
	}
	// Walk the QNAME starting at byte 12
	pos := 12
	for pos < len(dnsQuery) {
		l := int(dnsQuery[pos])
		pos++
		if l == 0 {
			break
		}
		if (l & 0xc0) != 0 {
			// Compressed label in single question — unexpected, let upstream handle it
			return nil, false
		}
		pos += l
	}
	if pos+4 > len(dnsQuery) {
		return nil, false
	}
	qtype := binary.BigEndian.Uint16(dnsQuery[pos : pos+2])
	qclass := binary.BigEndian.Uint16(dnsQuery[pos+2 : pos+4])
	if qtype != 28 || qclass != 1 {
		return nil, false
	}

	questionEnd := pos + 4
	resp := make([]byte, questionEnd)
	copy(resp, dnsQuery[:questionEnd])
	// Set QR=1 (response), RD preserved, RA=1 (recursion available), RCODE=0 (NOERROR)
	respFlags := (flags & 0x0100) | 0x8080
	binary.BigEndian.PutUint16(resp[2:4], respFlags)
	binary.BigEndian.PutUint16(resp[4:6], 1)   // QDCOUNT = 1
	binary.BigEndian.PutUint16(resp[6:8], 0)   // ANCOUNT = 0
	binary.BigEndian.PutUint16(resp[8:10], 0)  // NSCOUNT = 0
	binary.BigEndian.PutUint16(resp[10:12], 0) // ARCOUNT = 0
	return resp, true
}

// forwardDNS resolves one raw DNS query through the H2 tunnel. The primary
// path is DNS-over-HTTPS on :443 — the port the data plane already uses for
// everything else; CONNECT proxies that refuse egress to :53 were observed
// to kill all browser traffic while IP-based apps (Telegram) kept working.
// Classic DNS-over-TCP to 1.1.1.1:53 remains as fallback.
func (e *Engine) forwardDNS(dnsQuery []byte) ([]byte, error) {
	if synth, ok := synthesizeEmptyAAAA(dnsQuery); ok {
		return synth, nil
	}
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

func (c *streamConn) LocalAddr() net.Addr              { return streamAddr("h2-stream") }
func (c *streamConn) RemoteAddr() net.Addr             { return streamAddr("h2-stream") }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// forwardDNSOverHTTPS sends the raw DNS message as an RFC 8484
// application/dns-message POST to Cloudflare DoH, over TLS inside an H2
// CONNECT stream to 1.1.1.1:443. When Cloudflare negotiates ALPN "h2",
// queries run concurrently over the multiplexed http2.ClientConn without
// holding dohMu during the network round-trip (eliminating DNS Head-of-Line
// blocking when opening pages with 10+ parallel DNS lookups). Falls back
// seamlessly to serialized HTTP/1.1 when ALPN h2 is not negotiated.
func (e *Engine) forwardDNSOverHTTPS(dnsQuery []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		e.dohMu.Lock()
		if e.dohConn == nil || (e.dohH2CC != nil && !e.dohH2CC.CanTakeNewRequest()) {
			e.closeDoHLocked()
			conn, h2cc, cc, err := e.dialDoH()
			if err != nil {
				e.dohMu.Unlock()
				return nil, err
			}
			e.dohConn = conn
			e.dohH2CC = h2cc
			if h2cc == nil {
				e.dohReader = bufio.NewReader(conn)
			}
			e.dohCC = cc
		}
		h2cc := e.dohH2CC
		conn := e.dohConn
		reader := e.dohReader
		servingCC := e.dohCC
		if h2cc != nil {
			// Release dohMu before the network round-trip so parallel DNS
			// queries multiplex concurrently over the single H2 DoH stream.
			e.dohMu.Unlock()
			resp, err := e.dohRoundTripH2(h2cc, conn, dnsQuery)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			e.dohMu.Lock()
			if e.dohConn == conn {
				e.closeDoHLocked()
				e.dohCC = nil
			}
			e.dohMu.Unlock()
			if servingCC != nil {
				e.evictH2Conn(servingCC)
			}
			continue
		}

		// HTTP/1.1 fallback path (holds dohMu while reading/writing conn).
		resp, err := e.dohRoundTripBounded(conn, reader, dnsQuery)
		if err == nil {
			e.dohMu.Unlock()
			return resp, nil
		}
		lastErr = err
		e.closeDoHLocked()
		e.dohCC = nil
		e.dohMu.Unlock()
		if servingCC != nil {
			e.evictH2Conn(servingCC)
		}
	}
	return nil, lastErr
}

func (e *Engine) dialDoH() (*tls.Conn, *http2.ClientConn, *http2.ClientConn, error) {
	// The H2 stream must outlive this call (it backs the pooled DoH
	// connection), so it is opened with Background: openH2ConnectStream
	// binds the stream to the caller's context, and cancelling that
	// context would tear the stream down. Only the TLS handshake gets
	// its own short-lived context.
	rc, wc, cc, err := e.openH2ConnectStreamCC(context.Background(), "1.1.1.1:443")
	if err != nil {
		return nil, nil, nil, err
	}
	raw := &streamConn{Reader: rc, Writer: wc, rc: rc, wc: wc}
	cfg := &tls.Config{
		ServerName:         "cloudflare-dns.com",
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"h2", "http/1.1"},
		ClientSessionCache: currentSessionCache(),
	}
	if e.tlsCfg != nil {
		cfg = e.tlsCfg.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = "cloudflare-dns.com"
		}
	}
	tlsConn := tls.Client(raw, cfg)
	hsCtx, hsCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer hsCancel()
	if err := tlsConn.HandshakeContext(hsCtx); err != nil {
		_ = raw.Close()
		return nil, nil, nil, err
	}
	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		tr := &http2.Transport{
			ReadIdleTimeout: 15 * time.Second,
			PingTimeout:     5 * time.Second,
		}
		if h2cc, err := tr.NewClientConn(tlsConn); err == nil {
			return tlsConn, h2cc, cc, nil
		}
	}
	return tlsConn, nil, cc, nil
}

// dohExchangeTimeout bounds one DoH request/response exchange. The DoH
// connection rides an H2 stream, whose deadlines are no-ops — without
// this bound, an exchange on a path that silently died (device asleep,
// network gone) blocks forever. 4s is the compromise: the serialized
// era needed 6s because one stuck exchange queued every query behind
// it; with H2 multiplexing a slow exchange no longer blocks siblings,
// but the first exchange after sleep may also pay a session redial,
// which 2.5s did not reliably leave room for.
var dohExchangeTimeout = 4 * time.Second

type dohResult struct {
	body []byte
	err  error
}

// dohRoundTripH2 runs a concurrent RFC 8484 POST over an HTTP/2 ClientConn.
func (e *Engine) dohRoundTripH2(h2cc *http2.ClientConn, rawConn *tls.Conn, dnsQuery []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dohExchangeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://cloudflare-dns.com/dns-query", bytes.NewReader(dnsQuery))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	done := make(chan dohResult, 1)
	go func() {
		resp, rtErr := h2cc.RoundTrip(req)
		if rtErr != nil {
			done <- dohResult{err: rtErr}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			done <- dohResult{err: fmt.Errorf("doh status %s", resp.Status)}
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65535))
		if readErr != nil {
			done <- dohResult{err: readErr}
			return
		}
		if len(body) < 2 || len(dnsQuery) < 2 || body[0] != dnsQuery[0] || body[1] != dnsQuery[1] {
			done <- dohResult{err: errors.New("doh response txid mismatch")}
			return
		}
		done <- dohResult{body: body}
	}()

	select {
	case r := <-done:
		return r.body, r.err
	case <-ctx.Done():
		_ = rawConn.Close()
		return nil, errors.New("doh exchange timed out")
	}
}

// dohRoundTripBounded runs one DoH exchange with a hard timeout. On
// expiry it force-closes the connection — the only way to unblock a
// read/write stuck on the dead stream; the exchange goroutine's late
// result lands in the buffered channel and is discarded.
func (e *Engine) dohRoundTripBounded(conn *tls.Conn, reader *bufio.Reader, dnsQuery []byte) ([]byte, error) {
	done := make(chan dohResult, 1)
	go func() {
		body, err := dohRoundTripOn(conn, reader, dnsQuery)
		done <- dohResult{body: body, err: err}
	}()
	timer := time.NewTimer(dohExchangeTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.body, r.err
	case <-timer.C:
		_ = conn.Close()
		return nil, errors.New("doh exchange timed out")
	}
}

func dohRoundTripOn(conn *tls.Conn, reader *bufio.Reader, dnsQuery []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, "https://cloudflare-dns.com/dns-query", bytes.NewReader(dnsQuery))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(reader, req)
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
		e.dohH2CC = nil
	}
}

// forwardDNSOverTCP encapsulates a raw UDP DNS query in RFC 1035 2-byte length framing
// and sends it through the Fastly HTTP/2 CONNECT tunnel to Cloudflare DNS (1.1.1.1:53).
func (e *Engine) forwardDNSOverTCP(dnsQuery []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// sharedSessionCache is the TLS session-resumption cache shared by the
// H2 dialer and the DoH dialer. It lives at package level, so it survives
// engine Stop/Start cycles that reset everything else; resetSessionCache
// drops it on a fresh engine start so a service restart is equivalent
// to a process restart for transport state.
var (
	sessionCacheMu     sync.Mutex
	sharedSessionCache tls.ClientSessionCache = tls.NewLRUClientSessionCache(32)
)

func currentSessionCache() tls.ClientSessionCache {
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()
	return sharedSessionCache
}

func resetSessionCache() {
	sessionCacheMu.Lock()
	sharedSessionCache = tls.NewLRUClientSessionCache(32)
	sessionCacheMu.Unlock()
}

// pickH2ConnLocked selects the FIRST healthy HTTP/2 session and sticks
// to it. Deliberately not least-loaded balancing: each pooled session
// is a separate connection to the proxy with its own egress IP, so
// spreading streams across sessions makes the user's public IP flap
// from request to request (observed in the field 2026-11-16 after a
// brief least-loaded experiment — sites see a different IP on every
// page refresh). One sticky session keeps one stable egress IP until
// the session itself dies or fills up.
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
			ClientSessionCache: currentSessionCache(),
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

// connectHeaderTimeout caps how long a CONNECT waits for the proxy's
// response headers. On a live path the answer takes well under a second;
// on a dead one — the device switched networks (Wi-Fi <-> LTE) and the
// pooled session's socket now blackholes — RoundTrip would otherwise
// block for minutes of TCP retransmits. A var so tests can shrink it.
var connectHeaderTimeout = 10 * time.Second

// evictH2Conn drops cc from the pool and closes its raw connection. It is
// called when a CONNECT on a pooled session fails at the transport level:
// the session is a corpse and every later pick would hand it out again.
// Streams still open on it share its dead path, so closing the raw
// connection loses nothing that could still work.
func (e *Engine) evictH2Conn(cc *http2.ClientConn) {
	e.mu.Lock()
	kept := e.h2Pool[:0]
	for _, entry := range e.h2Pool {
		if entry.cc == cc {
			if entry.raw != nil {
				_ = entry.raw.Close()
			}
			continue
		}
		kept = append(kept, entry)
	}
	e.h2Pool = kept
	e.mu.Unlock()
	e.forgetSessionDeaths(cc)
}

// openH2ConnectStream opens an HTTP/2 CONNECT tunnel stream to targetAuthority.
// Uses URL.Opaque = targetAuthority and Host = targetAuthority so golang.org/x/net/http2
// sends `:method = CONNECT` and `:authority = targetAuthority` (without :scheme/:path).
func (e *Engine) openH2ConnectStream(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, error) {
	rc, wc, _, err := e.openH2ConnectStreamCC(ctx, targetAuthority)
	return rc, wc, err
}

// openH2ConnectStreamCC is openH2ConnectStream that also reports which
// pooled session serves the stream, so callers can blame mid-stream
// failures on the right session (see relayTCPStream).
//
// A genuine transport-level failure flushes the whole pool (every
// session shares the dead path), so the retry runs on a guaranteed-
// fresh session; a failure caused by our own recovery closing the
// session mid-CONNECT only evicts that session. That turns a network
// switch from "no internet until a manual VPN restart" into a single
// slightly slow connection — without letting recovery feed on itself.
func (e *Engine) openH2ConnectStreamCC(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, *http2.ClientConn, error) {
	// Up to three attempts: each transport failure removes at least the
	// one session it indicted (see openH2ConnectStreamOnce), so a pool
	// holding several corpses is cleared attempt by attempt instead of
	// by one pool-wide flush that also kills healthy streams.
	var lastErr error
	waitedAuth := false
	for attempt := 0; attempt < 3; attempt++ {
		rc, wc, cc, authPending, usedToken, err := e.openH2ConnectStreamOnce(ctx, targetAuthority)
		if err == nil {
			return rc, wc, cc, nil
		}
		var streamErr http2.StreamError
		if errors.As(err, &streamErr) {
			// Stream-scoped refusal (e.g. REFUSED_STREAM): the session
			// itself is healthy and the caller must see it as-is.
			return nil, nil, nil, err
		}
		var rejErr *rejectionError
		if authPending && !waitedAuth && errors.As(err, &rejErr) && rejErr.status == http.StatusUnauthorized {
			// The held token is dead and a refresh is in flight (triggered
			// either by this stream or a sibling stream a millisecond ago):
			// wait on the broadcast channel for the fresh token to land
			// and retry on it instead of failing the stream. Without this,
			// the first page load after every wake died even though
			// recovery took ~2 s — the app saw a reset and fell back to
			// its offline page.
			waitedAuth = true
			if e.waitTokenChange(3*time.Second, usedToken) {
				continue
			}
		}
		lastErr = err
	}
	return nil, nil, nil, lastErr
}

func (e *Engine) openH2ConnectStreamOnce(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, *http2.ClientConn, bool, string, error) {
	// Time-box only session establishment (TCP dial + TLS handshake).
	// The request itself keeps the caller's context: cancelling it would
	// tear down the live stream.
	dialCtx, dialCancel := context.WithTimeout(ctx, 20*time.Second)
	cc, token, err := e.ensureH2Session(dialCtx)
	dialCancel()
	if err != nil {
		return nil, nil, nil, false, "", err
	}

	pr, pw := io.Pipe()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetAuthority},
		Host:   targetAuthority,
		Header: make(http.Header),
		Body:   pr,
	}
	// Cap the wait for the CONNECT response headers (see
	// connectHeaderTimeout). The timer is stopped the moment RoundTrip
	// returns, so an established stream keeps the caller's context and
	// is never torn down by it.
	reqCtx, reqCancel := context.WithCancel(ctx)
	req = req.WithContext(reqCtx)
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	timer := time.AfterFunc(connectHeaderTimeout, reqCancel)
	resp, err := cc.RoundTrip(req)
	timer.Stop()
	if err != nil {
		reqCancel()
		_ = pw.Close()
		var streamErr http2.StreamError
		if !errors.As(err, &streamErr) {
			if isClosedConnErr(err) {
				e.evictH2Conn(cc)
			} else {
				e.ResetSession()
			}
		}
		return nil, nil, nil, false, token, err
	}

	if e.OnQuotaHeaders != nil && resp.Header != nil {
		e.OnQuotaHeaders(resp.Header)
	}
	if resp.StatusCode == http.StatusTooManyRequests && e.OnQuotaExhaust != nil {
		go e.OnQuotaExhaust()
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		authPending := false
		if resp.StatusCode == http.StatusUnauthorized {
			authPending = e.noteAuthFailure()
		}
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		_ = pw.Close()
		reqCancel()
		return nil, nil, nil, authPending, token, &rejectionError{
			target: targetAuthority,
			status: resp.StatusCode,
			body:   string(snippet),
		}
	}
	// The request context is deliberately NOT cancelled when the stream
	// closes (reqCancel lives until the parent context ends): in
	// x/net/http2, cancelling a live stream's context resets the stream,
	// and the write half can close before the response has finished —
	// a cancel-on-close wrapper (tried and reverted 2026-11-16, when
	// the egress IP changed on every page refresh) risks resetting
	// healthy streams and churning sessions. The timer above already
	// bounds the only phase the context exists for, the header wait.
	return resp.Body, pw, cc, false, token, nil
}

// ProbeLatency measures the latency to a Fastly proxy node as the total time
// from the start of the TCP dial until the TLS 1.3 ALPN h2 handshake completes
// on the same connection (which also verifies the node is live).
func ProbeLatency(ctx context.Context, proxyEndpoint, _ string) (time.Duration, error) {
	return ProbeTLSHandshake(ctx, proxyEndpoint)
}

// ProbeTLSHandshake returns the full dial+handshake duration, NOT the bare TCP
// connect RTT. Time-boxed to 2.5s so unreachable/filtered nodes do not hold
// up latency scans.
func ProbeTLSHandshake(ctx context.Context, proxyEndpoint string) (time.Duration, error) {
	host, _, err := net.SplitHostPort(proxyEndpoint)
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()

	dialer := &net.Dialer{Timeout: 2500 * time.Millisecond}
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
