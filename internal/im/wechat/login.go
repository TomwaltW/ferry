package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
)

const (
	// qrPollInterval is how often the QR code status is polled, on the clock.
	qrPollInterval = 2 * time.Second
	// maxQRRefreshes is how many expired QR codes are replaced before giving up.
	maxQRRefreshes = 3
	// botTypeILink is the bot_type query parameter of get_bot_qrcode.
	botTypeILink = "3"
)

// login drives the scan-to-login handshake. Start fetches a code, Wait polls
// its status until the user confirms it.
type login struct {
	cli *client
	clk clock.Clock
	log *slog.Logger

	base string // may move to the redirect host mid-handshake
	qr   string // key of the code currently on screen
}

func newLogin(cfg config.WeChatConfig, clk clock.Clock) *login {
	base := cfg.BaseURL
	if base == "" {
		base = ilinkwire.DefaultBaseURL
	}
	version := cfg.ChannelVersion
	if version == "" {
		version = ilinkwire.DefaultChannelVersion
	}
	if clk == nil {
		clk = clock.Real()
	}
	return &login{
		cli:  newClient(base, "", version),
		clk:  clk,
		log:  slog.Default(),
		base: base,
	}
}

// Start asks for a QR code and returns the payload the code has to encode.
func (l *login) Start(ctx context.Context) (string, error) {
	var resp ilinkwire.QRCodeResp
	q := url.Values{"bot_type": []string{botTypeILink}}
	if err := l.cli.get(ctx, l.base, ilinkwire.PathGetBotQRCode, q, &resp); err != nil {
		return "", fmt.Errorf("wechat: get qr code: %w", err)
	}
	if resp.QRCode == "" {
		return "", fmt.Errorf("wechat: gateway returned an empty qr code")
	}
	l.qr = resp.QRCode
	return resp.QRCodeImgContent, nil
}

// Wait polls the code until it is confirmed. An expired code is replaced at
// most maxQRRefreshes times, and onQR is called with every replacement.
func (l *login) Wait(ctx context.Context, onQR func(qrContent string)) (ports.Credentials, error) {
	if l.qr == "" {
		return ports.Credentials{}, fmt.Errorf("wechat: login not started")
	}

	refreshes := 0
	scanned := false
	for {
		select {
		case <-ctx.Done():
			return ports.Credentials{}, ctx.Err()
		case <-l.clk.After(qrPollInterval):
		}

		var st ilinkwire.QRStatusResp
		q := url.Values{"qrcode": []string{l.qr}}
		if err := l.cli.get(ctx, l.base, ilinkwire.PathGetQRCodeStatus, q, &st); err != nil {
			return ports.Credentials{}, fmt.Errorf("wechat: qr status: %w", err)
		}

		switch st.Status {
		case "wait":
		case "scaned":
			if !scanned {
				scanned = true
				l.log.Info("wechat login: qr code scanned, waiting for confirmation")
			}
		case "scaned_but_redirect":
			if st.RedirectHost != "" {
				l.base = "https://" + st.RedirectHost
				l.log.Info("wechat login: following redirect", "host", st.RedirectHost)
			}
		case "expired":
			if refreshes >= maxQRRefreshes {
				return ports.Credentials{}, fmt.Errorf("wechat: qr code expired %d times, giving up", refreshes)
			}
			refreshes++
			content, err := l.Start(ctx)
			if err != nil {
				return ports.Credentials{}, err
			}
			l.log.Info("wechat login: qr code refreshed", "attempt", refreshes)
			if onQR != nil {
				onQR(content)
			}
		case "confirmed":
			return l.credentials(st), nil
		default:
			return ports.Credentials{}, fmt.Errorf("wechat: unknown qr status %q", st.Status)
		}
	}
}

func (l *login) credentials(st ilinkwire.QRStatusResp) ports.Credentials {
	base := st.BaseURL
	if base == "" {
		base = l.base
	}
	return ports.Credentials{
		BotToken: st.BotToken,
		BotID:    st.ILinkBotID,
		OwnerID:  st.ILinkUserID,
		BaseURL:  base,
	}
}

var _ ports.Login = (*login)(nil)
