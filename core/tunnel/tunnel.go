// Package tunnel runs the local loopback SOCKS5 server (for Android Tun2Socks)
// and multiplexes outbound TCP and DNS traffic over an HTTP/2 TLS 1.3 CONNECT
// session to the selected Mozilla Fastly proxy (*.m1.fastly-masque.net:2499)
// using the active account's Guardian ProxyPass bearer JWT.
//
// Features & Optimizations:
//   - Correct HTTP/2 CONNECT :authority pseudo-header (URL.Opaque = targetAuthority)
//   - Built-in UDP-over-TCP (hev-socks5-tunnel cmd=0x04 & SOCKS5 UDP Associate cmd=0x03)
//     DNS-to-TCP bridge over the Fastly HTTP/2 tunnel so Android DNS resolves reliably
//   - Zero-allocation 32 KB buffer pool (sync.Pool + io.CopyBuffer)
//   - Live byte counting and X-Quota-* header inspection for real-time account usage stats
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

type Engine struct {
	mu             sync.RWMutex
	dialMu         sync.Mutex
	listener       net.Listener
	proxyHost      string
	bearerToken    string
	h2Conn         *http2.ClientConn
	rawTLS         net.Conn
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
		return
	}

	switch cmd {
	case 0x01: // Standard TCP CONNECT
		authority := net.JoinHostPort(host, strconv.Itoa(int(port)))
		if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		_ = client.SetDeadline(time.Time{})
		e.proxyTCPStream(client, authority)

	case 0x04: // HevSocks5Tunnel UDP-in-TCP (udp: 'tcp')
		if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		_ = client.SetDeadline(time.Time{})
		e.handleHevUDPInTCP(client)

	case 0x03: // Standard RFC 1928 SOCKS5 UDP ASSOCIATE (udp: 'udp')
		e.handleStandardUDPAssociate(client)
	}
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

func (e *Engine) proxyTCPStream(client net.Conn, authority string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamReader, streamWriter, err := e.openH2ConnectStream(ctx, authority)
	if err != nil {
		return
	}
	defer streamWriter.Close()
	defer streamReader.Close()

	onBytes := e.OnTrafficBytes
	go func() {
		copyZeroAlloc(streamWriter, client, onBytes)
		_ = streamWriter.Close()
	}()
	copyZeroAlloc(client, streamReader, onBytes)
}

// handleHevUDPInTCP handles HevSocks5Tunnel's UDP-over-TCP framing (cmd = 0x04)
// and resolves DNS / UDP queries over TCP through the Fastly HTTP/2 proxy.
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

		go func(rawAddr, udpPayload []byte) {
			respPayload, err := e.forwardDNSOverTCP(udpPayload)
			if err != nil || len(respPayload) == 0 {
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

// handleStandardUDPAssociate handles RFC 1928 SOCKS5 UDP ASSOCIATE (cmd = 0x03).
func (e *Engine) handleStandardUDPAssociate(tcpClient net.Conn) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return
	}
	defer udpConn.Close()

	udpPort := udpConn.LocalAddr().(*net.UDPAddr).Port
	reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, byte(udpPort >> 8), byte(udpPort)}
	if _, err := tcpClient.Write(reply); err != nil {
		return
	}
	_ = tcpClient.SetDeadline(time.Time{})

	go func() {
		buf := make([]byte, 65535)
		for {
			n, clientAddr, err := udpConn.ReadFromUDP(buf)
			if err != nil || n < 10 {
				return
			}
			pkt := append([]byte(nil), buf[:n]...)
			go func(datagram []byte, src *net.UDPAddr) {
				atyp := datagram[3]
				hdrLen := 0
				switch atyp {
				case 0x01:
					hdrLen = 10
				case 0x04:
					hdrLen = 22
				case 0x03:
					if len(datagram) > 4 {
						hdrLen = 4 + 1 + int(datagram[4]) + 2
					}
				}
				if hdrLen == 0 || len(datagram) <= hdrLen {
					return
				}
				dnsQuery := datagram[hdrLen:]
				if e.OnTrafficBytes != nil {
					e.OnTrafficBytes(int64(len(dnsQuery)))
				}
				respDNS, err := e.forwardDNSOverTCP(dnsQuery)
				if err != nil || len(respDNS) == 0 {
					return
				}
				if e.OnTrafficBytes != nil {
					e.OnTrafficBytes(int64(len(respDNS)))
				}
				out := make([]byte, hdrLen+len(respDNS))
				copy(out[:hdrLen], datagram[:hdrLen])
				copy(out[hdrLen:], respDNS)
				_, _ = udpConn.WriteToUDP(out, src)
			}(pkt, clientAddr)
		}
	}()

	// Keep UDP association alive until the control TCP connection closes
	var dummy [1]byte
	_, _ = tcpClient.Read(dummy[:])
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

func (e *Engine) ensureH2Session(ctx context.Context) (*http2.ClientConn, string, error) {
	e.mu.RLock()
	cc := e.h2Conn
	token := e.bearerToken
	proxyHost := e.proxyHost
	e.mu.RUnlock()

	if cc != nil && cc.CanTakeNewRequest() {
		return cc, token, nil
	}

	e.dialMu.Lock()
	defer e.dialMu.Unlock()

	e.mu.RLock()
	if e.h2Conn != nil && e.h2Conn.CanTakeNewRequest() {
		cc = e.h2Conn
		token = e.bearerToken
		e.mu.RUnlock()
		return cc, token, nil
	}
	proxyHost = e.proxyHost
	token = e.bearerToken
	e.mu.RUnlock()

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

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"h2"},
		ClientSessionCache: tls.NewLRUClientSessionCache(32),
	})
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
	e.closeH2Locked()
	e.rawTLS = tlsConn
	e.h2Conn = newCC
	token = e.bearerToken
	e.mu.Unlock()

	return newCC, token, nil
}

// openH2ConnectStream opens an HTTP/2 CONNECT tunnel stream to targetAuthority.
// Uses URL.Opaque = targetAuthority and Host = targetAuthority so golang.org/x/net/http2
// sends `:method = CONNECT` and `:authority = targetAuthority` (without :scheme/:path).
func (e *Engine) openH2ConnectStream(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, error) {
	cc, token, err := e.ensureH2Session(ctx)
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
		_ = resp.Body.Close()
		_ = pw.Close()
		return nil, nil, fmt.Errorf("CONNECT rejected HTTP %d", resp.StatusCode)
	}
	return resp.Body, pw, nil
}

// ProbeLatency measures the real 1-RTT latency to a Fastly proxy node.
// First measures TCP SYN -> SYN-ACK RTT (true 1-RTT network latency) and completes
// the TLS 1.3 ALPN h2 handshake on the same connection to verify the node is live.
func ProbeLatency(ctx context.Context, proxyEndpoint, _ string) (time.Duration, error) {
	return ProbeTLSHandshake(ctx, proxyEndpoint)
}

// ProbeTLSHandshake measures real 1-RTT TCP connect time and verifies TLS 1.3 ALPN h2 support.
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
	tcpRTT := time.Since(start)

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
	if tcpRTT < time.Millisecond {
		tcpRTT = time.Millisecond
	}
	return tcpRTT, nil
}
