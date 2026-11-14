// Package tunnel runs the local loopback SOCKS5 server (for Android Tun2Socks)
// and multiplexes outbound connections over an HTTP/2 TLS 1.3 CONNECT session
// to the selected Mozilla Fastly proxy (*.m1.fastly-masque.net:2499) using the
// active account's Guardian ProxyPass bearer JWT.
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

// UpdateToken hot-swaps the active Mozilla account's ProxyPass JWT without dropping the tunnel.
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
		if e.rawTLS != nil {
			_ = e.rawTLS.Close()
			e.rawTLS = nil
			e.h2Conn = nil
		}
	}
	e.mu.Unlock()
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listener != nil {
		_ = e.listener.Close()
		e.listener = nil
	}
	if e.rawTLS != nil {
		_ = e.rawTLS.Close()
		e.rawTLS = nil
		e.h2Conn = nil
	}
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
	_, _ = client.Write([]byte{0x05, 0x00})

	var reqHdr [4]byte
	if _, err := io.ReadFull(client, reqHdr[:]); err != nil || reqHdr[0] != 0x05 || reqHdr[1] != 0x01 {
		return
	}

	var host string
	switch reqHdr[3] {
	case 0x01: // IPv4
		var ip [4]byte
		if _, err := io.ReadFull(client, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03: // Domain
		var dlen [1]byte
		if _, err := io.ReadFull(client, dlen[:]); err != nil {
			return
		}
		domain := make([]byte, int(dlen[0]))
		if _, err := io.ReadFull(client, domain); err != nil {
			return
		}
		host = string(domain)
	case 0x04: // IPv6
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

	// Reply SOCKS5 succeeded
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
		_, _ = io.Copy(streamWriter, client)
		_ = streamWriter.Close()
	}()
	_, _ = io.Copy(client, streamReader)
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

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h2"},
		},
	}
	tlsConn, err := dialer.DialContext(ctx, "tcp", e.proxyHost)
	if err != nil {
		return nil, "", "", err
	}

	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, "", "", err
	}

	e.rawTLS = tlsConn
	e.h2Conn = cc
	return cc, e.proxyHost, e.bearerToken, nil
}

// openH2ConnectStream opens an HTTP/2 CONNECT tunnel stream to targetAuthority.
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

// ProbeLatency measures the round-trip time of an HTTP/2 CONNECT to www.google.com:443
// through the given Mozilla Fastly proxy endpoint.
func ProbeLatency(ctx context.Context, proxyEndpoint, bearerToken string) (time.Duration, error) {
	eng := NewEngine()
	eng.proxyHost = proxyEndpoint
	eng.bearerToken = bearerToken
	defer eng.Stop()

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Warm up TLS 1.3 + HTTP/2 session to Fastly edge
	if _, _, _, err := eng.ensureH2Session(probeCtx); err != nil {
		return 0, err
	}

	// Measure actual CONNECT round-trip to exit node
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
