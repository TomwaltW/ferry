package fakeilink

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

// Verbatim bodies from the failure surface of the spec, so that clients see
// exactly the bytes the real gateway sends.
const (
	bodyOK            = `{}`
	bodyUnauthorized  = `{"ret":-1,"errmsg":"unauthorized"}`
	bodyBadRequest    = `{"ret":-1,"errmsg":"bad request"}`
	bodySessionGone   = `{"ret":-14,"errcode":-14,"errmsg":"session expired"}`
	bodyBadToken      = `{"ret":-2,"errmsg":"invalid context_token"}`
	bodyWindowExpired = `{"ret":-2,"errmsg":"context_token expired"}`
	bodyNotImplement  = `{"ret":-1,"errmsg":"not implemented"}`
)

func normalizePath(p string) string { return strings.TrimPrefix(p, "/") }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/"+ilinkwire.PathGetBotQRCode, s.guard(s.handleGetQRCode))
	mux.HandleFunc("/"+ilinkwire.PathGetQRCodeStatus, s.guard(s.handleQRCodeStatus))
	mux.HandleFunc("/"+ilinkwire.PathGetUpdates, s.guard(s.authed(s.handleGetUpdates)))
	mux.HandleFunc("/"+ilinkwire.PathSendMessage, s.guard(s.authed(s.handleSendMessage)))
	mux.HandleFunc("/"+ilinkwire.PathGetConfig, s.guard(s.authed(s.handleGetConfig)))
	mux.HandleFunc("/"+ilinkwire.PathSendTyping, s.guard(s.authed(s.handleSendTyping)))
	mux.HandleFunc("/"+ilinkwire.PathGetUploadURL, s.guard(s.authed(s.handleGetUploadURL)))
	return mux
}

// guard consumes an armed fault for this path before the real handler runs.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		path := normalizePath(r.URL.Path)
		mode, armed := s.fails[path]
		if armed {
			delete(s.fails, path)
		}
		s.mu.Unlock()

		switch {
		case !armed:
			next(w, r)
		case mode == FailHTTP500:
			http.Error(w, "internal error", http.StatusInternalServerError)
		case mode == FailTimeout:
			select {
			case <-s.clk.After(hangDuration):
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			case <-s.closed:
			case <-r.Context().Done():
			}
		default:
			next(w, r)
		}
	}
}

// authed rejects a POST that does not carry both credential headers, and
// answers -14 while the session is marked expired.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		token, expired := s.botToken, s.sessionExpired
		s.mu.Unlock()

		authType := r.Header.Get(ilinkwire.HeaderAuthType)
		authz := r.Header.Get(ilinkwire.HeaderAuthorization)
		if authType != ilinkwire.AuthTypeValue || token == "" || authz != "Bearer "+token {
			writeRaw(w, http.StatusUnauthorized, bodyUnauthorized)
			return
		}
		if expired {
			writeRaw(w, http.StatusOK, bodySessionGone)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleGetQRCode(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	key := ids.New("qr")
	s.currentQR = key
	s.qrImg[key] = "https://ilink.example/qr/" + key
	if s.pendingConfirm {
		// ConfirmScan ran before this code existed; honour it now.
		s.confirmedQR = key
		s.pendingConfirm = false
	}
	resp := ilinkwire.QRCodeResp{QRCode: key, QRCodeImgContent: s.qrImg[key]}
	s.mu.Unlock()

	writeJSON(w, resp)
}

func (s *Server) handleQRCodeStatus(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("qrcode")

	s.mu.Lock()
	defer s.mu.Unlock()

	resp := ilinkwire.QRStatusResp{}
	switch {
	case key != "" && key == s.confirmedQR:
		resp.Status = "confirmed"
		resp.BotToken = s.botToken
		resp.ILinkBotID = s.botID
		resp.BaseURL = s.URL
		resp.ILinkUserID = s.ownerID
	case key != "" && key == s.currentQR:
		resp.Status = "wait"
	default:
		// Superseded or never issued: the client has to ask for a new code.
		resp.Status = "expired"
	}
	writeJSON(w, resp)
}

func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	var req ilinkwire.GetUpdatesReq
	if err := decode(r, &req); err != nil {
		writeRaw(w, http.StatusBadRequest, bodyBadRequest)
		return
	}

	timeout := s.clk.After(s.opts.LongPoll)
	for {
		s.mu.Lock()
		from := s.indexOfLocked(req.GetUpdatesBuf)
		if from < len(s.msgs) {
			resp := ilinkwire.GetUpdatesResp{
				Msgs:                 append([]ilinkwire.Message(nil), s.msgs[from:]...),
				GetUpdatesBuf:        s.cursorAtLocked(len(s.msgs)),
				LongPollingTimeoutMs: s.opts.LongPoll.Milliseconds(),
			}
			s.mu.Unlock()
			writeJSON(w, resp)
			return
		}
		wake := s.pushed
		idle := ilinkwire.GetUpdatesResp{
			GetUpdatesBuf:        s.cursorAtLocked(from),
			LongPollingTimeoutMs: s.opts.LongPoll.Milliseconds(),
		}
		s.mu.Unlock()

		select {
		case <-wake: // a message landed, look again
		case <-timeout:
			writeJSON(w, idle)
			return
		case <-s.closed:
			writeJSON(w, idle)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req ilinkwire.SendMessageReq
	if err := decode(r, &req); err != nil || req.Msg == nil {
		writeRaw(w, http.StatusOK, bodyBadToken)
		return
	}
	msg := req.Msg

	s.mu.Lock()
	defer s.mu.Unlock()

	win, known := s.windows[msg.ContextToken]
	if !known {
		writeRaw(w, http.StatusOK, bodyBadToken)
		return
	}

	now := s.clk.Now()
	rec := SentRecord{
		ClientID:     msg.ClientID,
		ToUserID:     msg.ToUserID,
		ContextToken: msg.ContextToken,
		Text:         textOf(msg.ItemList),
		Items:        append([]ilinkwire.MessageItem(nil), msg.ItemList...),
		At:           now,
	}

	expired := now.Sub(win.createdAt) > s.opts.WindowTTL
	switch {
	case msg.ClientID != "" && s.seenIDs[msg.ClientID]:
		// Retransmission of a reply already accepted: recorded, never counted.
		rec.Duplicate = true
	case expired:
		if s.opts.ExpiredMode == ExpiredError {
			writeRaw(w, http.StatusOK, bodyWindowExpired)
			return
		}
		rec.Dropped = true
	case win.replies >= s.opts.ReplyLimit:
		rec.Dropped = true // the real gateway swallows these without a word
	default:
		win.replies++
	}

	if msg.ClientID != "" {
		s.seenIDs[msg.ClientID] = true
	}
	s.sent = append(s.sent, rec)
	writeRaw(w, http.StatusOK, bodyOK)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	var req ilinkwire.GetConfigReq
	if err := decode(r, &req); err != nil {
		writeRaw(w, http.StatusBadRequest, bodyBadRequest)
		return
	}
	writeJSON(w, ilinkwire.GetConfigResp{TypingTicket: typingTicket})
}

func (s *Server) handleSendTyping(w http.ResponseWriter, r *http.Request) {
	var req ilinkwire.SendTypingReq
	if err := decode(r, &req); err != nil {
		writeRaw(w, http.StatusBadRequest, bodyBadRequest)
		return
	}

	s.mu.Lock()
	s.typingCh = append(s.typingCh, TypingRecord{
		UserID: req.ILinkUserID,
		Status: req.Status,
		At:     s.clk.Now(),
	})
	s.mu.Unlock()

	writeRaw(w, http.StatusOK, bodyOK)
}

func (s *Server) handleGetUploadURL(w http.ResponseWriter, _ *http.Request) {
	writeRaw(w, http.StatusOK, bodyNotImplement)
}

// indexOfLocked maps a cursor to the position that follows it. An empty or
// unrecognised cursor replays the backlog from the start.
func (s *Server) indexOfLocked(cursor string) int {
	if cursor == "" {
		return 0
	}
	if at, ok := s.cursorAt[cursor]; ok {
		return at
	}
	return 0
}

// cursorAtLocked names a position in the backlog. The string is opaque: the
// same position always yields the same cursor, and nothing else is promised.
func (s *Server) cursorAtLocked(at int) string {
	if c, ok := s.cursorFor[at]; ok {
		return c
	}
	c := ids.New("cur")
	s.cursorFor[at] = c
	s.cursorAt[c] = at
	return c
}

func textOf(items []ilinkwire.MessageItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.TextItem != nil {
			b.WriteString(it.TextItem.Text)
		}
	}
	return b.String()
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("fakeilink: decode %s: %w", r.URL.Path, err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
