package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type nopCloserWriter struct{ io.Writer }

func (nopCloserWriter) Close() error { return nil }

// flushWriter flushes after every write so CONNECT echo streams behave
// interactively instead of buffering until close.
type flushWriter struct{ w http.ResponseWriter }

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

type testConnIdxKey struct{}

type testProxy struct {
	addr          string
	mu            sync.Mutex
	authorities   []string
	authHeaders   []string
	rejectHosts   map[string]bool
	dohHits       int
	dohPaths      []string
	ln            net.Listener
	connCount     int
	hangFirstConn bool
}

func startTestProxy(t *testing.T) *testProxy {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	p := &testProxy{rejectHosts: map[string]bool{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.NotFound(w, r)
			return
		}
		if idx, _ := r.Context().Value(testConnIdxKey{}).(int); idx == 0 {
			p.mu.Lock()
			hang := p.hangFirstConn
			p.mu.Unlock()
			if hang {
				// Corpse connection: the network path is gone and the
				// server never answers. Block until the client gives up
				// on the stream.
				<-r.Context().Done()
				return
			}
		}
		p.mu.Lock()
		p.authorities = append(p.authorities, r.Host)
		p.authHeaders = append(p.authHeaders, r.Header.Get("Proxy-Authorization"))
		reject := p.rejectHosts[r.Host]
		p.mu.Unlock()
		if reject {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if r.Host == "1.1.1.1:443" {
			// DoH endpoint: TLS inside the CONNECT stream, HTTP/1.1
			// /dns-query exchanges echoing the DNS message back.
			sc := &streamConn{
				Reader: r.Body,
				Writer: flushWriter{w},
				rc:     r.Body,
				wc:     nopCloserWriter{flushWriter{w}},
			}
			tlsConn := tls.Server(sc, &tls.Config{Certificates: []tls.Certificate{cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			br := bufio.NewReader(tlsConn)
			for {
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return
				}
				p.mu.Lock()
				p.dohHits++
				p.dohPaths = append(p.dohPaths, req.URL.Path)
				p.mu.Unlock()
				resp := &http.Response{
					Status:        "200 OK",
					StatusCode:    http.StatusOK,
					Proto:         "HTTP/1.1",
					ProtoMajor:    1,
					ProtoMinor:    1,
					Header:        http.Header{"Content-Type": []string{"application/dns-message"}},
					Body:          io.NopCloser(bytes.NewReader(body)),
					ContentLength: int64(len(body)),
				}
				if err := resp.Write(tlsConn); err != nil {
					return
				}
			}
		}
		if r.Host == "1.1.1.1:53" {
			// DNS-over-TCP endpoint: echo the framed query back as the response.
			var lenBuf [2]byte
			if _, err := io.ReadFull(r.Body, lenBuf[:]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(lenBuf[:]))
			query := make([]byte, n)
			if _, err := io.ReadFull(r.Body, query); err != nil {
				return
			}
			fw := flushWriter{w}
			_, _ = fw.Write(lenBuf[:])
			_, _ = fw.Write(query)
			return
		}
		// Generic target: byte echo.
		_, _ = io.Copy(flushWriter{w}, r.Body)
	})

	tlsLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.ln = tlsLn
	p.addr = tlsLn.Addr().String()

	go func() {
		srv := &http2.Server{}
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			idx := p.connCount
			p.connCount++
			p.mu.Unlock()
			go func() {
				// http2.Server.ServeConn requires the TLS handshake (and thus
				// ALPN "h2" negotiation) to be complete before it runs.
				if tc, ok := conn.(*tls.Conn); ok {
					if err := tc.Handshake(); err != nil {
						return
					}
				}
				ctx := context.WithValue(context.Background(), testConnIdxKey{}, idx)
				srv.ServeConn(conn, &http2.ServeConnOpts{Handler: handler, Context: ctx})
			}()
		}
	}()
	t.Cleanup(func() { _ = tlsLn.Close() })
	return p
}

func startTestEngine(t *testing.T, proxy *testProxy) (*Engine, int) {
	t.Helper()
	e := NewEngine()
	e.tlsCfg = &tls.Config{
		InsecureSkipVerify: true, // test-only self-signed proxy cert
		NextProtos:         []string{"h2"},
	}
	port, err := e.Start("127.0.0.1:0", proxy.addr, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e, port
}

func socksGreeting(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("bad greeting reply %v", resp)
	}
}

func socksRequest(t *testing.T, conn net.Conn, cmd byte, ip net.IP, port uint16) byte {
	t.Helper()
	req := []byte{0x05, cmd, 0x00, 0x01}
	req = append(req, ip.To4()...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], port)
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 0x05 {
		t.Fatalf("bad reply version %d", resp[0])
	}
	return resp[1]
}

func dialSocks(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSocksConnectEndToEnd(t *testing.T) {
	proxy := startTestProxy(t)
	_, port := startTestEngine(t, proxy)

	conn := dialSocks(t, port)
	socksGreeting(t, conn)
	rep := socksRequest(t, conn, 0x01, net.ParseIP("93.184.216.34"), 80)
	if rep != 0x00 {
		t.Fatalf("CONNECT rep = %d, want 0x00", rep)
	}

	msg := []byte("hello-vpn")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo mismatch: %q != %q", got, msg)
	}

	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if len(proxy.authorities) != 1 || proxy.authorities[0] != "93.184.216.34:80" {
		t.Fatalf(":authority seen by proxy = %v, want [93.184.216.34:80]", proxy.authorities)
	}
	if len(proxy.authHeaders) != 1 || proxy.authHeaders[0] != "Bearer test-token" {
		t.Fatalf("Proxy-Authorization = %v", proxy.authHeaders)
	}
}

// TestH2CorpseSessionEvictedAndRetried models a Wi-Fi <-> LTE switch: the
// pooled H2 session's path silently dies (the server stops answering on
// that connection while the socket still looks open). The next CONNECT
// must time out fast, evict the corpse and succeed on a fresh session —
// no manual restart.
func TestH2CorpseSessionEvictedAndRetried(t *testing.T) {
	old := connectHeaderTimeout
	connectHeaderTimeout = 300 * time.Millisecond
	defer func() { connectHeaderTimeout = old }()

	proxy := startTestProxy(t)
	e, _ := startTestEngine(t, proxy)

	// Phase A: a normal stream pools the first session.
	rc, wc, err := e.openH2ConnectStream(context.Background(), "93.184.216.34:80")
	if err != nil {
		t.Fatalf("phase A CONNECT: %v", err)
	}
	_ = wc.Close()
	_ = rc.Close()

	// The path under the pooled session dies.
	proxy.mu.Lock()
	proxy.hangFirstConn = true
	proxy.mu.Unlock()

	start := time.Now()
	rc, wc, err = e.openH2ConnectStream(context.Background(), "93.184.216.34:80")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("CONNECT after path death must recover on a fresh session: %v", err)
	}
	_ = wc.Close()
	_ = rc.Close()
	if elapsed < connectHeaderTimeout {
		t.Fatalf("recovery took %v — the corpse timeout never fired", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("recovery took %v — too slow", elapsed)
	}
	proxy.mu.Lock()
	conns := proxy.connCount
	proxy.mu.Unlock()
	if conns != 2 {
		t.Fatalf("proxy saw %d connections, want 2 (corpse + fresh)", conns)
	}
}

func TestSocksConnectRejectedReportsFailure(t *testing.T) {
	proxy := startTestProxy(t)
	proxy.rejectHosts["10.0.0.1:443"] = true
	_, port := startTestEngine(t, proxy)

	conn := dialSocks(t, port)
	socksGreeting(t, conn)
	rep := socksRequest(t, conn, 0x01, net.ParseIP("10.0.0.1"), 443)
	if rep == 0x00 {
		t.Fatal("rejected CONNECT must not report SOCKS success")
	}
}

func TestSocksUnsupportedCommand(t *testing.T) {
	proxy := startTestProxy(t)
	_, port := startTestEngine(t, proxy)

	conn := dialSocks(t, port)
	socksGreeting(t, conn)
	rep := socksRequest(t, conn, 0x03, net.ParseIP("0.0.0.0"), 0) // UDP ASSOCIATE
	if rep != 0x07 {
		t.Fatalf("UDP ASSOCIATE rep = %d, want 0x07 (command not supported)", rep)
	}
}

// runDNSBridgeScenario drives one FWD_UDP session: a DNS query to 8.8.8.8:53
// must come back framed from 8.8.8.8:53 with the payload intact, and a
// non-DNS datagram must be dropped.
func runDNSBridgeScenario(t *testing.T, port int) {
	t.Helper()

	conn := dialSocks(t, port)
	socksGreeting(t, conn)
	rep := socksRequest(t, conn, 0x05, net.ParseIP("0.0.0.0"), 0) // FWD_UDP
	if rep != 0x00 {
		t.Fatalf("FWD_UDP rep = %d, want 0x00", rep)
	}

	sendDatagram := func(dstIP string, dstPort uint16, payload []byte) {
		addr := []byte{0x01}
		addr = append(addr, net.ParseIP(dstIP).To4()...)
		var pb [2]byte
		binary.BigEndian.PutUint16(pb[:], dstPort)
		addr = append(addr, pb[:]...)
		frame := make([]byte, 3, 3+len(addr)+len(payload))
		binary.BigEndian.PutUint16(frame[0:2], uint16(len(payload)))
		frame[2] = byte(3 + len(addr))
		frame = append(frame, addr...)
		frame = append(frame, payload...)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	query := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sendDatagram("8.8.8.8", 53, query)

	// Expect one framed response echoing the query, addressed from 8.8.8.8:53.
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		t.Fatal(err)
	}
	dataLen := int(binary.BigEndian.Uint16(hdr[0:2]))
	hdrLen := int(hdr[2])
	rest := make([]byte, hdrLen-3+dataLen)
	if _, err := io.ReadFull(conn, rest); err != nil {
		t.Fatal(err)
	}
	respAddr, respPayload := rest[:hdrLen-3], rest[hdrLen-3:]
	host, p, err := parseSocksAddrBytes(respAddr)
	if err != nil || host != "8.8.8.8" || p != 53 {
		t.Fatalf("response addr = %v:%d err=%v", host, p, err)
	}
	if string(respPayload) != string(query) {
		t.Fatalf("DNS payload mismatch: %x != %x", respPayload, query)
	}

	// Non-DNS UDP (e.g. QUIC to 443) must be dropped, not bridged.
	sendDatagram("142.250.0.1", 443, []byte{0xaa, 0xbb})
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	one := make([]byte, 1)
	if _, err := conn.Read(one); err == nil {
		t.Fatal("non-DNS UDP datagram unexpectedly produced a response")
	}
}

func TestHevUDPInTCPDNSBridge(t *testing.T) {
	proxy := startTestProxy(t)
	_, port := startTestEngine(t, proxy)

	runDNSBridgeScenario(t, port)

	// With both paths open, DNS must go through DoH on :443 (primary).
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.dohHits < 1 {
		t.Fatalf("expected DoH to be used, dohHits = %d", proxy.dohHits)
	}
	if len(proxy.dohPaths) < 1 || proxy.dohPaths[0] != "/dns-query" {
		t.Fatalf("DoH paths = %v", proxy.dohPaths)
	}
}

func TestHevUDPInTCPDNSBridgeDoHOnly(t *testing.T) {
	proxy := startTestProxy(t)
	proxy.rejectHosts["1.1.1.1:53"] = true // proxy refuses :53 egress
	_, port := startTestEngine(t, proxy)

	runDNSBridgeScenario(t, port)

	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.dohHits < 1 {
		t.Fatal("DNS must still resolve via DoH when :53 is refused")
	}
}

func TestHevUDPInTCPDNSBridgeFallbackTCP(t *testing.T) {
	proxy := startTestProxy(t)
	proxy.rejectHosts["1.1.1.1:443"] = true // DoH unreachable
	_, port := startTestEngine(t, proxy)

	runDNSBridgeScenario(t, port)

	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.dohHits != 0 {
		t.Fatalf("DoH must not be hit when :443 is rejected, dohHits = %d", proxy.dohHits)
	}
}

func TestParseSocksAddrBytes(t *testing.T) {
	host, port, err := parseSocksAddrBytes([]byte{0x01, 1, 2, 3, 4, 0x00, 0x35})
	if err != nil || host != "1.2.3.4" || port != 53 {
		t.Fatalf("ipv4: %v %d %v", host, port, err)
	}
	dom := append([]byte{0x03, 0x0b}, []byte("example.com")...)
	dom = append(dom, 0x01, 0xbb)
	host, port, err = parseSocksAddrBytes(dom)
	if err != nil || host != "example.com" || port != 443 {
		t.Fatalf("domain: %v %d %v", host, port, err)
	}
	v6 := []byte{0x04}
	v6 = append(v6, net.ParseIP("2001:db8::1").To16()...)
	v6 = append(v6, 0x00, 0x50)
	host, port, err = parseSocksAddrBytes(v6)
	if err != nil || host != "2001:db8::1" || port != 80 {
		t.Fatalf("ipv6: %v %d %v", host, port, err)
	}
	if _, _, err := parseSocksAddrBytes([]byte{0x09, 1}); err == nil {
		t.Fatal("unknown atyp must error")
	}
	if _, _, err := parseSocksAddrBytes([]byte{0x01, 1, 2}); err == nil {
		t.Fatal("truncated ipv4 must error")
	}
}
