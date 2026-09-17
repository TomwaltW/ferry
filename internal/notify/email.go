package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"

	"github.com/TomwaltW/ferry/internal/config"
)

// defaultSMTPPort is the submission port; Ferry always upgrades it with
// STARTTLS before it sends anything (spec-p2 5.4).
const defaultSMTPPort = 587

type emailChannel struct {
	cfg      config.EmailConfig
	password string
	port     int
	// tlsConfig is the STARTTLS configuration. Production leaves it nil, which
	// verifies the server against the system roots; the test in this package
	// points it at its own self-signed certificate.
	tlsConfig *tls.Config
	dial      func(ctx context.Context, addr string) (net.Conn, error)
}

func newEmail(cfg config.EmailConfig) (channel, error) {
	switch {
	case cfg.Host == "":
		return nil, errors.New("notify: email needs notify.email.host")
	case cfg.From == "":
		return nil, errors.New("notify: email needs notify.email.from")
	case len(cfg.To) == 0:
		return nil, errors.New("notify: email needs notify.email.to")
	}
	port := cfg.Port
	if port == 0 {
		port = defaultSMTPPort
	}
	return &emailChannel{
		cfg:      cfg,
		password: os.Getenv(cfg.PasswordEnv),
		port:     port,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}, nil
}

func (e *emailChannel) name() string { return "email" }

func (e *emailChannel) send(ctx context.Context, msg note) error {
	addr := net.JoinHostPort(e.cfg.Host, strconv.Itoa(e.port))
	conn, err := e.dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("notify: email: dial %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("notify: email: %w", err)
	}
	defer c.Close()

	tlsCfg := e.tlsConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: e.cfg.Host, MinVersion: tls.VersionTLS12}
	}
	// STARTTLS is not optional: the password and the reminder would otherwise
	// cross the network in the clear.
	if err := c.StartTLS(tlsCfg); err != nil {
		return fmt.Errorf("notify: email: starttls: %w", err)
	}
	if e.cfg.User != "" && e.password != "" {
		if err := c.Auth(smtp.PlainAuth("", e.cfg.User, e.password, e.cfg.Host)); err != nil {
			return fmt.Errorf("notify: email: auth: %w", err)
		}
	}
	if err := c.Mail(e.cfg.From); err != nil {
		return fmt.Errorf("notify: email: from: %w", err)
	}
	for _, to := range e.cfg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("notify: email: rcpt %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("notify: email: data: %w", err)
	}
	if _, err := w.Write(e.message(msg)); err != nil {
		w.Close()
		return fmt.Errorf("notify: email: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: email: close: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("notify: email: quit: %w", err)
	}
	return nil
}

// message renders the reminder as a minimal UTF-8 mail. The subject goes
// through RFC 2047 because a Chinese title is not a legal header otherwise.
func (e *emailChannel) message(msg note) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", e.cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(e.cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", msg.Title))
	fmt.Fprintf(&b, "Date: %s\r\n", msg.At.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	// A bare line of "." would end the message early.
	for _, line := range strings.Split(msg.Body, "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(strings.TrimRight(line, "\r") + "\r\n")
	}
	return []byte(b.String())
}
