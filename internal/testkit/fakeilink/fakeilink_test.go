package fakeilink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

var epoch = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

const owner = "user-owner"

// newLoggedIn starts a server that already holds a bot token, which is where
// every test but TestLoginFlow begins.
func newLoggedIn(t *testing.T, opts Options) *Server {
	t.Helper()
	s := New(opts)
	t.Cleanup(s.Close)
	s.ConfirmScan(owner)
	return s
}

func authHeaders(s *Server) map[string]string {
	return map[string]string{
		ilinkwire.HeaderAuthType:      ilinkwire.AuthTypeValue,
		ilinkwire.HeaderAuthorization: "Bearer " + s.BotToken(),
		ilinkwire.HeaderUIN:           "1234567890",
	}
}

// request speaks to the server without a *testing.T, so that goroutines can
// call it and report through a channel instead of through t.Fatal.
func request(method, url string, body any, hdr map[string]string) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, raw, nil
}

func do(t *testing.T, method, url string, body any, hdr map[string]string) (int, []byte) {
	t.Helper()
	status, raw, err := request(method, url, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	return status, raw
}

func decodeInto[T any](raw []byte) (T, error) {
	var out T
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode %q: %w", raw, err)
	}
	return out, nil
}

func post[T any](t *testing.T, s *Server, path string, body any, hdr map[string]string) (int, T) {
	t.Helper()
	status, raw := do(t, http.MethodPost, s.URL+"/"+path, body, hdr)
	out, err := decodeInto[T](raw)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return status, out
}

func get[T any](t *testing.T, s *Server, path, query string) T {
	t.Helper()
	url := s.URL + "/" + path
	if query != "" {
		url += "?" + query
	}
	status, raw := do(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %q", path, status, raw)
	}
	out, err := decodeInto[T](raw)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return out
}

// reply builds the bot-side message a driver sends back on a context_token.
func reply(contextToken, clientID, text string) ilinkwire.SendMessageReq {
	return ilinkwire.SendMessageReq{
		Msg: &ilinkwire.Message{
			ToUserID:     owner,
			ClientID:     clientID,
			ContextToken: contextToken,
			MessageType:  ilinkwire.MsgTypeBot,
			MessageState: ilinkwire.StateFinish,
			ItemList: []ilinkwire.MessageItem{{
				Type:     ilinkwire.ItemText,
				TextItem: &ilinkwire.TextItem{Text: text},
			}},
		},
		BaseInfo: &ilinkwire.BaseInfo{ChannelVersion: ilinkwire.DefaultChannelVersion},
	}
}

func send(t *testing.T, s *Server, contextToken, clientID, text string) (int, ilinkwire.APIError) {
	t.Helper()
	return post[ilinkwire.APIError](t, s, ilinkwire.PathSendMessage, reply(contextToken, clientID, text), authHeaders(s))
}

// pollOnce runs one getupdates without a *testing.T, for use off the test
// goroutine.
func pollOnce(s *Server, cursor string) (ilinkwire.GetUpdatesResp, error) {
	status, raw, err := request(http.MethodPost, s.URL+"/"+ilinkwire.PathGetUpdates,
		ilinkwire.GetUpdatesReq{GetUpdatesBuf: cursor}, authHeaders(s))
	if err != nil {
		return ilinkwire.GetUpdatesResp{}, err
	}
	if status != http.StatusOK {
		return ilinkwire.GetUpdatesResp{}, fmt.Errorf("getupdates: status %d, body %q", status, raw)
	}
	resp, err := decodeInto[ilinkwire.GetUpdatesResp](raw)
	if err != nil {
		return resp, err
	}
	if resp.Failed() {
		return resp, fmt.Errorf("getupdates failed: %+v", resp.APIError)
	}
	return resp, nil
}

func poll(t *testing.T, s *Server, cursor string) ilinkwire.GetUpdatesResp {
	t.Helper()
	resp, err := pollOnce(s, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLoginFlow(t *testing.T) {
	s := New(Options{})
	t.Cleanup(s.Close)

	first := get[ilinkwire.QRCodeResp](t, s, ilinkwire.PathGetBotQRCode, "bot_type=3")
	if first.QRCode == "" || first.QRCodeImgContent == "" {
		t.Fatalf("get_bot_qrcode returned %+v, want both fields set", first)
	}

	second := get[ilinkwire.QRCodeResp](t, s, ilinkwire.PathGetBotQRCode, "bot_type=3")
	if second.QRCode == first.QRCode {
		t.Errorf("two calls handed out the same qrcode %q, want a fresh one each time", second.QRCode)
	}
	if second.QRCodeImgContent == first.QRCodeImgContent {
		t.Errorf("two calls handed out the same image content %q", second.QRCodeImgContent)
	}

	status := get[ilinkwire.QRStatusResp](t, s, ilinkwire.PathGetQRCodeStatus, "qrcode="+second.QRCode)
	if status.Status != "wait" {
		t.Errorf("status before ConfirmScan = %q, want wait", status.Status)
	}
	if status.BotToken != "" {
		t.Errorf("status before ConfirmScan leaked bot token %q", status.BotToken)
	}
	if superseded := get[ilinkwire.QRStatusResp](t, s, ilinkwire.PathGetQRCodeStatus, "qrcode="+first.QRCode); superseded.Status != "expired" {
		t.Errorf("superseded qrcode status = %q, want expired", superseded.Status)
	}

	s.ConfirmScan(owner)

	status = get[ilinkwire.QRStatusResp](t, s, ilinkwire.PathGetQRCodeStatus, "qrcode="+second.QRCode)
	if status.Status != "confirmed" {
		t.Fatalf("status after ConfirmScan = %q, want confirmed", status.Status)
	}
	if status.BotToken == "" || status.BotToken != s.BotToken() {
		t.Errorf("bot_token = %q, want the server's %q", status.BotToken, s.BotToken())
	}
	if status.ILinkBotID != s.BotID() {
		t.Errorf("ilink_bot_id = %q, want %q", status.ILinkBotID, s.BotID())
	}
	if status.BaseURL != s.URL {
		t.Errorf("baseurl = %q, want %q", status.BaseURL, s.URL)
	}
	if status.ILinkUserID != owner {
		t.Errorf("ilink_user_id = %q, want %q", status.ILinkUserID, owner)
	}

	// A second scan re-issues the token, which is how re-login recovers.
	before := s.BotToken()
	get[ilinkwire.QRCodeResp](t, s, ilinkwire.PathGetBotQRCode, "bot_type=3")
	s.ConfirmScan(owner)
	if s.BotToken() == before {
		t.Errorf("ConfirmScan reused bot token %q, want a new one", before)
	}
}

func TestLongPoll(t *testing.T) {
	const window = 300 * time.Millisecond
	s := newLoggedIn(t, Options{LongPoll: window})

	t.Run("backlog returns at once", func(t *testing.T) {
		token := s.PushText(owner, "hello")

		start := time.Now()
		resp := poll(t, s, "")
		elapsed := time.Since(start)

		if elapsed > time.Second {
			t.Errorf("a pending message took %v to arrive, want well under a second", elapsed)
		}
		if len(resp.Msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(resp.Msgs))
		}
		got := resp.Msgs[0]
		if got.ContextToken != token {
			t.Errorf("context_token = %q, want the one PushText returned, %q", got.ContextToken, token)
		}
		if got.MessageType != ilinkwire.MsgTypeUser {
			t.Errorf("message_type = %d, want %d", got.MessageType, ilinkwire.MsgTypeUser)
		}
		if got.MessageID == 0 || got.Seq == 0 {
			t.Errorf("message_id=%d seq=%d, want both assigned", got.MessageID, got.Seq)
		}
		if got.FromUserID != owner {
			t.Errorf("from_user_id = %q, want %q", got.FromUserID, owner)
		}
		if text := textOf(got.ItemList); text != "hello" {
			t.Errorf("text = %q, want %q", text, "hello")
		}
		if resp.GetUpdatesBuf == "" {
			t.Error("response carried no cursor")
		}
		if resp.LongPollingTimeoutMs != window.Milliseconds() {
			t.Errorf("longpolling_timeout_ms = %d, want %d", resp.LongPollingTimeoutMs, window.Milliseconds())
		}
	})

	t.Run("idle holds for LongPoll then returns empty", func(t *testing.T) {
		cursor := poll(t, s, "").GetUpdatesBuf

		start := time.Now()
		resp := poll(t, s, cursor)
		elapsed := time.Since(start)

		if len(resp.Msgs) != 0 {
			t.Errorf("got %d messages, want none", len(resp.Msgs))
		}
		if elapsed < window {
			t.Errorf("idle poll returned after %v, want it held for at least %v", elapsed, window)
		}
		if resp.LongPollingTimeoutMs != window.Milliseconds() {
			t.Errorf("longpolling_timeout_ms = %d, want %d", resp.LongPollingTimeoutMs, window.Milliseconds())
		}
	})

	t.Run("the cursor advances past delivered messages", func(t *testing.T) {
		cursor := poll(t, s, "").GetUpdatesBuf
		s.PushText(owner, "second")

		resp := poll(t, s, cursor)
		if len(resp.Msgs) != 1 {
			t.Fatalf("got %d messages, want only the new one", len(resp.Msgs))
		}
		if text := textOf(resp.Msgs[0].ItemList); text != "second" {
			t.Errorf("text = %q, want %q", text, "second")
		}
		if resp.GetUpdatesBuf == cursor {
			t.Error("cursor did not move after a delivery")
		}
	})

	t.Run("a push wakes a parked poll", func(t *testing.T) {
		slow := newLoggedIn(t, Options{LongPoll: 30 * time.Second})
		slow.PushText(owner, "backlog") // drained at once, so this poll does not park
		cursor := poll(t, slow, "").GetUpdatesBuf

		type result struct {
			resp ilinkwire.GetUpdatesResp
			err  error
		}
		done := make(chan result, 1)
		go func() {
			resp, err := pollOnce(slow, cursor)
			done <- result{resp, err}
		}()

		time.Sleep(50 * time.Millisecond) // let the poll park
		slow.PushText(owner, "woken")

		select {
		case got := <-done:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if len(got.resp.Msgs) != 1 {
				t.Fatalf("parked poll returned %d messages, want the pushed one", len(got.resp.Msgs))
			}
			if text := textOf(got.resp.Msgs[0].ItemList); text != "woken" {
				t.Errorf("text = %q, want %q", text, "woken")
			}
		case <-time.After(time.Second):
			t.Fatal("a push did not wake the parked poll within a second")
		}
	})

	t.Run("Close releases a parked poll", func(t *testing.T) {
		slow := New(Options{LongPoll: 30 * time.Second})
		slow.ConfirmScan(owner)

		done := make(chan struct{})
		go func() {
			defer close(done)
			pollOnce(slow, "") //nolint:errcheck // the teardown path is what is under test
		}()

		time.Sleep(50 * time.Millisecond)
		slow.Close()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Close left a long poll hanging")
		}
	})
}

func TestSend(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "ping")

	status, apiErr := send(t, s, token, "cid-1", "pong")
	if status != http.StatusOK || apiErr.Failed() {
		t.Fatalf("sendmessage: status %d, %+v, want 200 and an empty body", status, apiErr)
	}

	sent := s.Sent()
	if len(sent) != 1 {
		t.Fatalf("Sent() has %d records, want 1", len(sent))
	}
	rec := sent[0]
	if rec.ClientID != "cid-1" || rec.ContextToken != token || rec.ToUserID != owner || rec.Text != "pong" {
		t.Errorf("record = %+v, want the reply as sent", rec)
	}
	if len(rec.Items) != 1 || rec.Items[0].Type != ilinkwire.ItemText {
		t.Errorf("items = %+v, want the single text item", rec.Items)
	}
	if rec.Duplicate || rec.Dropped {
		t.Errorf("record = %+v, want neither duplicate nor dropped", rec)
	}
	if rec.At.IsZero() {
		t.Error("record carries no timestamp")
	}

	// The same client_id again is recorded, but never counted twice.
	if _, apiErr := send(t, s, token, "cid-1", "pong"); apiErr.Failed() {
		t.Fatalf("resend answered %+v, want an empty body", apiErr)
	}
	sent = s.Sent()
	if len(sent) != 2 {
		t.Fatalf("Sent() has %d records, want 2", len(sent))
	}
	if !sent[1].Duplicate {
		t.Errorf("second arrival of cid-1 = %+v, want Duplicate", sent[1])
	}
	if sent[0].Duplicate {
		t.Errorf("first arrival of cid-1 = %+v, want it left alone", sent[0])
	}

	// An unknown context_token is rejected outright and leaves no record.
	_, apiErr = send(t, s, "ctx_nosuchtoken", "cid-2", "stray")
	if apiErr.Ret != -2 || apiErr.ErrMsg != "invalid context_token" {
		t.Errorf("unknown context_token answered %+v, want ret=-2 invalid context_token", apiErr)
	}
	if len(s.Sent()) != 2 {
		t.Errorf("Sent() has %d records, want the rejected call to leave none", len(s.Sent()))
	}
}

func TestReplyLimit(t *testing.T) {
	s := newLoggedIn(t, Options{ReplyLimit: 2})
	token := s.PushText(owner, "ping")

	for _, cid := range []string{"a", "b", "c"} {
		status, apiErr := send(t, s, token, cid, "reply "+cid)
		if status != http.StatusOK || apiErr.Failed() {
			t.Fatalf("send %s: status %d, %+v, want the gateway to swallow it silently", cid, status, apiErr)
		}
	}

	sent := s.Sent()
	if len(sent) != 3 {
		t.Fatalf("Sent() has %d records, want 3", len(sent))
	}
	for i, want := range []bool{false, false, true} {
		if sent[i].Dropped != want {
			t.Errorf("record %d Dropped = %v, want %v", i, sent[i].Dropped, want)
		}
	}

	// A duplicate consumes no budget, so raising the limit by one admits
	// exactly one further reply.
	s.SetReplyLimit(3)
	if _, apiErr := send(t, s, token, "a", "reply a"); apiErr.Failed() {
		t.Fatalf("duplicate after SetReplyLimit answered %+v", apiErr)
	}
	if _, apiErr := send(t, s, token, "d", "reply d"); apiErr.Failed() {
		t.Fatalf("send d answered %+v", apiErr)
	}
	sent = s.Sent()
	if len(sent) != 5 {
		t.Fatalf("Sent() has %d records, want 5", len(sent))
	}
	if !sent[3].Duplicate || sent[3].Dropped {
		t.Errorf("record 3 = %+v, want a duplicate that was not dropped", sent[3])
	}
	if sent[4].Dropped {
		t.Errorf("record 4 = %+v, want it admitted by the raised limit", sent[4])
	}

	// Each context_token carries its own budget.
	other := s.PushText(owner, "another")
	if _, apiErr := send(t, s, other, "e", "fresh window"); apiErr.Failed() {
		t.Fatalf("send on a fresh window answered %+v", apiErr)
	}
	if last := s.Sent()[5]; last.Dropped {
		t.Errorf("record 5 = %+v, want a fresh window to start with a full budget", last)
	}
}

func TestExpire(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "ping")

	s.ExpireSession()

	_, apiErr := send(t, s, token, "cid-1", "pong")
	if apiErr.Ret != ilinkwire.ErrCodeSessionExpired || apiErr.ErrCode != ilinkwire.ErrCodeSessionExpired {
		t.Errorf("sendmessage after ExpireSession = %+v, want ret and errcode -14", apiErr)
	}
	if !apiErr.SessionExpired() {
		t.Error("the body does not read as an expired session to ilinkwire")
	}
	if len(s.Sent()) != 0 {
		t.Errorf("Sent() has %d records, want none while the session is expired", len(s.Sent()))
	}

	status, updates := post[ilinkwire.GetUpdatesResp](t, s, ilinkwire.PathGetUpdates, ilinkwire.GetUpdatesReq{}, authHeaders(s))
	if status != http.StatusOK {
		t.Errorf("getupdates status = %d, want 200 carrying an error body", status)
	}
	if !updates.SessionExpired() {
		t.Errorf("getupdates after ExpireSession = %+v, want -14", updates.APIError)
	}

	// The QR flow stays open so that the client can log in again.
	code := get[ilinkwire.QRCodeResp](t, s, ilinkwire.PathGetBotQRCode, "bot_type=3")
	s.ConfirmScan(owner)
	if st := get[ilinkwire.QRStatusResp](t, s, ilinkwire.PathGetQRCodeStatus, "qrcode="+code.QRCode); st.Status != "confirmed" {
		t.Fatalf("status after re-scan = %q, want confirmed", st.Status)
	}
	if _, apiErr := send(t, s, token, "cid-2", "pong"); apiErr.Failed() {
		t.Errorf("sendmessage after re-scan = %+v, want it accepted again", apiErr)
	}
}

func TestWindowTTL(t *testing.T) {
	f := clock.NewFake(epoch)
	s := newLoggedIn(t, Options{WindowTTL: time.Hour, Clock: f})
	token := s.PushText(owner, "ping")

	f.Advance(30 * time.Minute)
	if _, apiErr := send(t, s, token, "inside", "still open"); apiErr.Failed() {
		t.Fatalf("send inside the window = %+v, want it accepted", apiErr)
	}
	if rec := s.Sent()[0]; rec.Dropped {
		t.Errorf("record = %+v, want a reply inside the window kept", rec)
	} else if !rec.At.Equal(epoch.Add(30 * time.Minute)) {
		t.Errorf("record timestamp = %v, want the fake clock's %v", rec.At, epoch.Add(30*time.Minute))
	}

	f.Advance(31 * time.Minute) // now past WindowTTL
	status, apiErr := send(t, s, token, "outside", "too late")
	if status != http.StatusOK || apiErr.Failed() {
		t.Fatalf("send past the window = status %d %+v, want it swallowed silently", status, apiErr)
	}
	if rec := s.Sent()[1]; !rec.Dropped {
		t.Errorf("record = %+v, want Dropped past the window", rec)
	}

	// A window opened later is still inside its own TTL.
	fresh := s.PushText(owner, "ping again")
	if _, apiErr := send(t, s, fresh, "fresh", "in time"); apiErr.Failed() {
		t.Fatalf("send on a window opened later = %+v, want it accepted", apiErr)
	}
	if rec := s.Sent()[2]; rec.Dropped {
		t.Errorf("record = %+v, want a freshly opened window to accept replies", rec)
	}

	t.Run("ExpiredError answers -2", func(t *testing.T) {
		f := clock.NewFake(epoch)
		s := newLoggedIn(t, Options{WindowTTL: time.Hour, ExpiredMode: ExpiredError, Clock: f})
		token := s.PushText(owner, "ping")

		f.Advance(2 * time.Hour)
		status, apiErr := send(t, s, token, "outside", "too late")
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200 carrying an error body", status)
		}
		if apiErr.Ret != -2 || apiErr.ErrMsg != "context_token expired" {
			t.Errorf("answer = %+v, want ret=-2 context_token expired", apiErr)
		}
		if len(s.Sent()) != 0 {
			t.Errorf("Sent() has %d records, want the rejected call to leave none", len(s.Sent()))
		}
	})
}

func TestAuth(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "ping")
	body := reply(token, "cid-1", "pong")

	for _, tc := range []struct {
		name string
		hdr  map[string]string
	}{
		{"no headers", nil},
		{"no AuthorizationType", map[string]string{ilinkwire.HeaderAuthorization: "Bearer " + s.BotToken()}},
		{"no Authorization", map[string]string{ilinkwire.HeaderAuthType: ilinkwire.AuthTypeValue}},
		{"wrong AuthorizationType", map[string]string{
			ilinkwire.HeaderAuthType:      "basic",
			ilinkwire.HeaderAuthorization: "Bearer " + s.BotToken(),
		}},
		{"wrong token", map[string]string{
			ilinkwire.HeaderAuthType:      ilinkwire.AuthTypeValue,
			ilinkwire.HeaderAuthorization: "Bearer btok_notthetoken",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, apiErr := post[ilinkwire.APIError](t, s, ilinkwire.PathSendMessage, body, tc.hdr)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
			if apiErr.Ret != -1 || apiErr.ErrMsg != "unauthorized" {
				t.Errorf("body = %+v, want ret=-1 unauthorized", apiErr)
			}
		})
	}

	if len(s.Sent()) != 0 {
		t.Errorf("Sent() has %d records, want none from rejected calls", len(s.Sent()))
	}

	t.Run("every POST path is guarded", func(t *testing.T) {
		for _, path := range []string{
			ilinkwire.PathGetUpdates,
			ilinkwire.PathSendMessage,
			ilinkwire.PathGetConfig,
			ilinkwire.PathSendTyping,
			ilinkwire.PathGetUploadURL,
		} {
			if status, _ := post[ilinkwire.APIError](t, s, path, map[string]any{}, nil); status != http.StatusUnauthorized {
				t.Errorf("%s without credentials: status %d, want 401", path, status)
			}
		}
	})

	t.Run("the QR flow needs no credentials", func(t *testing.T) {
		fresh := New(Options{})
		t.Cleanup(fresh.Close)
		if code := get[ilinkwire.QRCodeResp](t, fresh, ilinkwire.PathGetBotQRCode, "bot_type=3"); code.QRCode == "" {
			t.Error("get_bot_qrcode demanded credentials, want it open before login")
		}
	})

	t.Run("credentials accepted", func(t *testing.T) {
		status, apiErr := post[ilinkwire.APIError](t, s, ilinkwire.PathSendMessage, body, authHeaders(s))
		if status != http.StatusOK || apiErr.Failed() {
			t.Errorf("status %d, %+v, want the call accepted", status, apiErr)
		}
	})
}

func TestConfigAndTyping(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "ping")

	_, cfg := post[ilinkwire.GetConfigResp](t, s, ilinkwire.PathGetConfig,
		ilinkwire.GetConfigReq{ILinkUserID: owner, ContextToken: token}, authHeaders(s))
	if cfg.TypingTicket == "" {
		t.Fatal("getconfig returned no typing_ticket")
	}

	for _, state := range []int{1, 2} {
		_, apiErr := post[ilinkwire.APIError](t, s, ilinkwire.PathSendTyping,
			ilinkwire.SendTypingReq{ILinkUserID: owner, TypingTicket: cfg.TypingTicket, Status: state}, authHeaders(s))
		if apiErr.Failed() {
			t.Fatalf("sendtyping %d answered %+v", state, apiErr)
		}
	}

	typing := s.Typing()
	if len(typing) != 2 {
		t.Fatalf("Typing() has %d records, want 2", len(typing))
	}
	if typing[0].UserID != owner || typing[0].Status != 1 || typing[1].Status != 2 {
		t.Errorf("Typing() = %+v, want the two calls in order", typing)
	}
}

func TestUploadURLRejectsIncompleteRequest(t *testing.T) {
	s := newLoggedIn(t, Options{})
	// Neither a filekey nor a key to encrypt with: there is nothing to hand
	// out an upload address for.
	_, apiErr := post[ilinkwire.APIError](t, s, ilinkwire.PathGetUploadURL, map[string]any{}, authHeaders(s))
	if apiErr.Ret != -1 || apiErr.ErrMsg != "bad request" {
		t.Errorf("getuploadurl = %+v, want ret=-1 bad request", apiErr)
	}
}

func TestFailNext(t *testing.T) {
	s := newLoggedIn(t, Options{})
	token := s.PushText(owner, "ping")

	s.FailNext(ilinkwire.PathSendMessage, FailHTTP500)
	status, _ := do(t, http.MethodPost, s.URL+"/"+ilinkwire.PathSendMessage, reply(token, "cid-1", "pong"), authHeaders(s))
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 from the injected fault", status)
	}
	if len(s.Sent()) != 0 {
		t.Errorf("Sent() has %d records, want the failed call recorded nowhere", len(s.Sent()))
	}

	// The fault is one shot: the next call goes through.
	if _, apiErr := send(t, s, token, "cid-1", "pong"); apiErr.Failed() {
		t.Errorf("call after the injected fault = %+v, want it accepted", apiErr)
	}

	t.Run("FailTimeout parks the request until Close", func(t *testing.T) {
		hung := New(Options{})
		hung.ConfirmScan(owner)
		hung.FailNext("/"+ilinkwire.PathGetConfig, FailTimeout) // a leading slash is accepted

		done := make(chan struct{})
		go func() {
			defer close(done)
			request(http.MethodPost, hung.URL+"/"+ilinkwire.PathGetConfig, //nolint:errcheck // the teardown path is what is under test
				ilinkwire.GetConfigReq{}, authHeaders(hung))
		}()

		select {
		case <-done:
			t.Fatal("FailTimeout answered at once, want the request parked")
		case <-time.After(100 * time.Millisecond):
		}

		hung.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Close left the parked request hanging")
		}
	})
}

func TestConcurrentUse(t *testing.T) {
	s := newLoggedIn(t, Options{LongPoll: 50 * time.Millisecond, ReplyLimit: 1000})

	const senders, repliesEach = 8, 4
	errs := make(chan error, senders)
	var wg sync.WaitGroup
	for i := range senders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := s.PushText(owner, "ping")
			for j := range repliesEach {
				_, _, err := request(http.MethodPost, s.URL+"/"+ilinkwire.PathSendMessage,
					reply(token, fmt.Sprintf("cid-%d-%d", i, j), "pong"), authHeaders(s))
				if err != nil {
					errs <- err
					return
				}
			}
			if _, err := pollOnce(s, ""); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got, want := len(s.Sent()), senders*repliesEach; got != want {
		t.Errorf("Sent() has %d records, want %d", got, want)
	}
	for _, rec := range s.Sent() {
		if rec.Duplicate || rec.Dropped {
			t.Errorf("record = %+v, want every distinct reply kept", rec)
		}
	}
}
