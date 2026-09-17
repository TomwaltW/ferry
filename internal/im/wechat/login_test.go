package wechat

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

type loginResult struct {
	creds ports.Credentials
	err   error
}

// waitInBackground runs Wait so the test can drive the clock meanwhile.
func waitInBackground(lg *login, ctx context.Context, onQR func(string)) chan loginResult {
	res := make(chan loginResult, 1)
	go func() {
		creds, err := lg.Wait(ctx, onQR)
		res <- loginResult{creds, err}
	}()
	return res
}

func TestLoginConfirm(t *testing.T) {
	s := fakeilink.New(fakeilink.Options{})
	t.Cleanup(s.Close)

	fake := clock.NewFake(epoch)
	lg, ok := NewLogin(config.WeChatConfig{BaseURL: s.URL}, fake).(*login)
	if !ok {
		t.Fatal("NewLogin did not return the wechat login")
	}
	lg.log = discardLogger()

	ctx := context.Background()
	content, err := lg.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.Contains(content, "/qr/") {
		t.Fatalf("Start returned %q, want the qr image content", content)
	}

	res := waitInBackground(lg, ctx, nil)

	// One poll that still says "wait": the login keeps going.
	fake.BlockUntil(1)
	fake.Advance(qrPollInterval)

	fake.BlockUntil(1)
	s.ConfirmScan(owner)
	fake.Advance(qrPollInterval)

	got := <-res
	if got.err != nil {
		t.Fatalf("Wait: %v", got.err)
	}
	want := ports.Credentials{
		BotToken: s.BotToken(),
		BotID:    s.BotID(),
		OwnerID:  owner,
		BaseURL:  s.URL, // baseurl from the gateway wins over the dialled host
	}
	if got.creds != want {
		t.Fatalf("credentials = %+v, want %+v", got.creds, want)
	}
}

func TestLoginRedirect(t *testing.T) {
	const redirectHost = "redirect.ilink.example"

	var secondCalls atomic.Int32
	second := stub(t, func(_ *stubServer, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+ilinkwire.PathGetQRCodeStatus {
			t.Errorf("redirect host got %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		secondCalls.Add(1)
		writeJSON(t, w, ilinkwire.QRStatusResp{
			Status:      "confirmed",
			BotToken:    "btok-redirected",
			ILinkBotID:  "bot-redirected",
			ILinkUserID: owner,
		})
	})

	var statusCalls atomic.Int32
	first := stub(t, func(_ *stubServer, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + ilinkwire.PathGetBotQRCode:
			writeJSON(t, w, ilinkwire.QRCodeResp{QRCode: "qr-1", QRCodeImgContent: "content-1"})
		case "/" + ilinkwire.PathGetQRCodeStatus:
			if statusCalls.Add(1) == 1 {
				writeJSON(t, w, ilinkwire.QRStatusResp{Status: "scaned"})
				return
			}
			writeJSON(t, w, ilinkwire.QRStatusResp{
				Status:       "scaned_but_redirect",
				RedirectHost: redirectHost,
			})
		default:
			http.NotFound(w, r)
		}
	})

	target, err := url.Parse(second.URL)
	if err != nil {
		t.Fatalf("parse redirect target: %v", err)
	}

	fake := clock.NewFake(epoch)
	lg := newLogin(config.WeChatConfig{BaseURL: first.URL}, fake)
	lg.log = discardLogger()
	lg.cli.http = &http.Client{Transport: rewriteHost{host: redirectHost, target: target}}

	ctx := context.Background()
	if _, err := lg.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := waitInBackground(lg, ctx, nil)

	for i := 0; i < 3; i++ { // scaned, scaned_but_redirect, then the new host
		fake.BlockUntil(1)
		fake.Advance(qrPollInterval)
	}

	got := <-res
	if got.err != nil {
		t.Fatalf("Wait: %v", got.err)
	}
	if secondCalls.Load() != 1 {
		t.Fatalf("redirect host polled %d times, want 1", secondCalls.Load())
	}
	if got.creds.BotToken != "btok-redirected" || got.creds.BotID != "bot-redirected" {
		t.Fatalf("credentials = %+v, want the redirected gateway's", got.creds)
	}
	// No baseurl in the answer, so the redirect host itself is the base.
	if want := "https://" + redirectHost; got.creds.BaseURL != want {
		t.Fatalf("BaseURL = %q, want %q", got.creds.BaseURL, want)
	}
}

func TestLoginRefreshExpired(t *testing.T) {
	t.Run("refresh then confirm", func(t *testing.T) {
		s := fakeilink.New(fakeilink.Options{})
		t.Cleanup(s.Close)

		fake := clock.NewFake(epoch)
		lg := newLogin(config.WeChatConfig{BaseURL: s.URL}, fake)
		lg.log = discardLogger()

		ctx := context.Background()
		if _, err := lg.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}

		// Someone else asks for a code, which supersedes ours: the gateway now
		// answers "expired" for the code on screen.
		var superseded ilinkwire.QRCodeResp
		if err := lg.cli.get(ctx, s.URL, ilinkwire.PathGetBotQRCode, url.Values{"bot_type": {botTypeILink}}, &superseded); err != nil {
			t.Fatalf("supersede the code: %v", err)
		}

		refreshed := make(chan string, maxQRRefreshes)
		res := waitInBackground(lg, ctx, func(content string) { refreshed <- content })

		fake.BlockUntil(1)
		fake.Advance(qrPollInterval) // expired, so a new code is fetched

		select {
		case content := <-refreshed:
			if !strings.Contains(content, "/qr/") {
				t.Fatalf("onQR got %q, want the new qr image content", content)
			}
		case <-time.After(recvTimeout):
			t.Fatal("onQR was never called")
		}

		fake.BlockUntil(1)
		s.ConfirmScan(owner)
		fake.Advance(qrPollInterval)

		got := <-res
		if got.err != nil {
			t.Fatalf("Wait: %v", got.err)
		}
		if got.creds.BotToken != s.BotToken() || got.creds.OwnerID != owner {
			t.Fatalf("credentials = %+v, want the confirmed session", got.creds)
		}
		if len(refreshed) != 0 {
			t.Fatalf("%d extra refreshes", len(refreshed))
		}
	})

	t.Run("gives up after three", func(t *testing.T) {
		var codes atomic.Int32
		srv := stub(t, func(_ *stubServer, w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/" + ilinkwire.PathGetBotQRCode:
				n := codes.Add(1)
				writeJSON(t, w, ilinkwire.QRCodeResp{
					QRCode:           "qr-" + strconv.Itoa(int(n)),
					QRCodeImgContent: "content-" + strconv.Itoa(int(n)),
				})
			case "/" + ilinkwire.PathGetQRCodeStatus:
				writeJSON(t, w, ilinkwire.QRStatusResp{Status: "expired"})
			default:
				http.NotFound(w, r)
			}
		})

		fake := clock.NewFake(epoch)
		lg := newLogin(config.WeChatConfig{BaseURL: srv.URL}, fake)
		lg.log = discardLogger()

		ctx := context.Background()
		if _, err := lg.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}

		var refreshes atomic.Int32
		res := waitInBackground(lg, ctx, func(string) { refreshes.Add(1) })

		for i := 0; i < maxQRRefreshes+1; i++ {
			fake.BlockUntil(1)
			fake.Advance(qrPollInterval)
		}

		got := <-res
		if got.err == nil {
			t.Fatalf("Wait returned %+v, want an error after %d refreshes", got.creds, maxQRRefreshes)
		}
		if refreshes.Load() != maxQRRefreshes {
			t.Fatalf("onQR called %d times, want %d", refreshes.Load(), maxQRRefreshes)
		}
	})
}
