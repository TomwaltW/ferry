package notify

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is the smallest server that can accept one STARTTLS submission, so
// that the e-mail channel is exercised end to end without a real mail host.
type fakeSMTP struct {
	addr     string
	roots    *x509.CertPool
	listener net.Listener

	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	auth     string
	starttls bool
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	cert, pool := selfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{addr: ln.Addr().String(), roots: pool, listener: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, cert)
		}
	}()
	return s
}

// port returns the port the fake is listening on.
func (s *fakeSMTP) port(t *testing.T) int {
	t.Helper()
	_, p, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("split %q: %v", s.addr, err)
	}
	var n int
	for _, r := range p {
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *fakeSMTP) serve(conn net.Conn, cert tls.Certificate) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	write := func(line string) {
		rw.WriteString(line + "\r\n")
		rw.Flush()
	}
	write("220 fake ESMTP")
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			s.mu.Lock()
			started := s.starttls
			s.mu.Unlock()
			if started {
				write("250-fake")
				write("250 AUTH PLAIN")
				continue
			}
			write("250-fake")
			write("250 STARTTLS")
		case upper == "STARTTLS":
			write("220 ready")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			s.mu.Lock()
			s.starttls = true
			s.mu.Unlock()
			conn = tlsConn
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
			write = func(line string) {
				rw.WriteString(line + "\r\n")
				rw.Flush()
			}
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			s.mu.Lock()
			s.auth = strings.TrimSpace(cmd[len("AUTH PLAIN"):])
			s.mu.Unlock()
			write("235 accepted")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = addrOf(cmd)
			s.mu.Unlock()
			write("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, addrOf(cmd))
			s.mu.Unlock()
			write("250 ok")
		case upper == "DATA":
			write("354 go ahead")
			var body strings.Builder
			for {
				l, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				body.WriteString(l)
			}
			s.mu.Lock()
			s.data = body.String()
			s.mu.Unlock()
			write("250 queued")
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

// addrOf pulls the address out of "MAIL FROM:<a@b>".
func addrOf(cmd string) string {
	_, rest, ok := strings.Cut(cmd, ":")
	if !ok {
		return ""
	}
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "<")
	if i := strings.Index(rest, ">"); i != -1 {
		rest = rest[:i]
	}
	return rest
}

func (s *fakeSMTP) snapshot() (from string, rcpts []string, data, auth string, started bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.from, append([]string(nil), s.rcpts...), s.data, s.auth, s.starttls
}

// decodedAuth splits the PLAIN credential the client sent.
func (s *fakeSMTP) decodedAuth(t *testing.T) (user, password string) {
	t.Helper()
	_, _, _, auth, _ := s.snapshot()
	if auth == "" {
		return "", ""
	}
	raw, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		t.Fatalf("decode auth %q: %v", auth, err)
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		t.Fatalf("auth payload = %q, want identity\\0user\\0password", raw)
	}
	return parts[1], parts[2]
}

// selfSigned makes a certificate for 127.0.0.1 and the pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ferry-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
