// Package fakeilink is an in-process stand-in for the iLink bot gateway. It
// speaks the subset of the protocol Ferry uses and can be told to misbehave.
// P0-T1 implements the behaviour; this file only fixes the contract.
package fakeilink

import (
	"net/http/httptest"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

type ExpiredMode string

const (
	ExpiredDrop  ExpiredMode = "drop"  // default: accept silently but never deliver
	ExpiredError ExpiredMode = "error" // answer ret=-2
)

type FailMode int

const (
	FailTimeout FailMode = iota + 1 // hang for 60 seconds or until the server closes
	FailHTTP500
)

type Options struct {
	LongPoll    time.Duration // default 2s (the real gateway holds for about 35s)
	ReplyLimit  int           // replies accepted per context_token, default 10
	WindowTTL   time.Duration // lifetime of a context_token, default 24h
	ExpiredMode ExpiredMode   // default ExpiredDrop
	Clock       clock.Clock   // default clock.Real(); window expiry is measured with it
}

type SentRecord struct {
	ClientID, ToUserID, ContextToken, Text string
	Items                                  []ilinkwire.MessageItem
	Duplicate                              bool // same client_id arriving a second time or later
	Dropped                                bool // silently discarded: over the limit or past the window
	At                                     time.Time
}

type TypingRecord struct {
	UserID string
	Status int
	At     time.Time
}

// hangDuration is how long FailTimeout holds a request, measured on the
// injected clock.
const hangDuration = 60 * time.Second

// defaults applied to the zero value of Options.
const (
	defaultLongPoll   = 2 * time.Second
	defaultReplyLimit = 10
	defaultWindowTTL  = 24 * time.Hour
)

// typingTicket is the fixed ticket handed out by getconfig.
const typingTicket = "fake-typing-ticket"

// window is the reply budget bound to one context_token.
type window struct {
	createdAt time.Time
	replies   int
}

type Server struct {
	URL string

	http   *httptest.Server
	opts   Options
	clk    clock.Clock
	closed chan struct{}
	once   sync.Once

	mu sync.Mutex

	// login state
	botID          string
	botToken       string
	ownerID        string
	currentQR      string
	confirmedQR    string
	qrImg          map[string]string // qrcode key -> image content
	pendingConfirm bool              // ConfirmScan arrived before any QR was handed out
	sessionExpired bool

	// inbound messages and their opaque cursors
	msgs      []ilinkwire.Message
	cursorFor map[int]string // index into msgs -> cursor naming that position
	cursorAt  map[string]int // cursor -> index into msgs
	nextSeq   int64
	nextMsgID int64
	pushed    chan struct{} // closed and replaced whenever a message arrives

	// outbound traffic
	windows  map[string]*window
	seenIDs  map[string]bool // client_id seen before, across every window
	sent     []SentRecord
	typingCh []TypingRecord

	// media: keys promised at getuploadurl time, and the objects the CDN holds
	uploadKeys map[string]string     // filekey -> aes key, hexadecimal
	objects    map[string]*cdnObject // encrypted_query_param -> stored object

	// fault injection, one shot per path
	fails map[string]FailMode
}

// New starts a gateway on a local address. Every field of opts that is left at
// its zero value falls back to the default documented on Options.
func New(opts Options) *Server {
	if opts.LongPoll <= 0 {
		opts.LongPoll = defaultLongPoll
	}
	if opts.ReplyLimit <= 0 {
		opts.ReplyLimit = defaultReplyLimit
	}
	if opts.WindowTTL <= 0 {
		opts.WindowTTL = defaultWindowTTL
	}
	if opts.ExpiredMode == "" {
		opts.ExpiredMode = ExpiredDrop
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}

	s := &Server{
		opts:       opts,
		clk:        opts.Clock,
		closed:     make(chan struct{}),
		botID:      ids.New("bot"),
		qrImg:      map[string]string{},
		cursorFor:  map[int]string{},
		cursorAt:   map[string]int{},
		pushed:     make(chan struct{}),
		windows:    map[string]*window{},
		seenIDs:    map[string]bool{},
		uploadKeys: map[string]string{},
		objects:    map[string]*cdnObject{},
		fails:      map[string]FailMode{},
	}
	s.http = httptest.NewServer(s.routes())
	s.URL = s.http.URL
	return s
}

// Close releases every request still parked in a long poll or an injected
// timeout, then shuts the listener down.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closed) // handlers must return before httptest waits on them
		s.http.Close()
	})
}

// ConfirmScan moves the current QR code to confirmed and issues a new bot token.
func (s *Server) ConfirmScan(ownerUserID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.botToken = ids.New("btok")
	s.ownerID = ownerUserID
	s.sessionExpired = false
	if s.currentQR != "" && s.currentQR != s.confirmedQR {
		s.confirmedQR = s.currentQR
		s.pendingConfirm = false
		return
	}
	// No QR code is waiting yet. Confirm the next one instead of stranding a
	// client that has not reached get_bot_qrcode.
	s.pendingConfirm = true
}

func (s *Server) BotToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botToken
}

func (s *Server) BotID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botID
}

func (s *Server) PushText(fromUserID, text string) (contextToken string) {
	return s.PushMessage(ilinkwire.Message{
		FromUserID: fromUserID,
		ItemList: []ilinkwire.MessageItem{{
			Type:     ilinkwire.ItemText,
			TextItem: &ilinkwire.TextItem{Text: text},
		}},
	})
}

// PushMessage queues an inbound message for the next getupdates. The server
// owns seq, message_id, message_type and context_token; everything else is
// taken from m as given.
func (s *Server) PushMessage(m ilinkwire.Message) (contextToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextSeq++
	s.nextMsgID++
	token := ids.New("ctx")

	m.Seq = s.nextSeq
	m.MessageID = s.nextMsgID
	m.MessageType = ilinkwire.MsgTypeUser
	m.ContextToken = token
	if m.ToUserID == "" {
		m.ToUserID = s.botID
	}
	if m.CreateTimeMs == 0 {
		m.CreateTimeMs = s.clk.Now().UnixMilli()
	}
	for i := range m.ItemList {
		if m.ItemList[i].MsgID == "" {
			m.ItemList[i].MsgID = ids.New("item")
		}
	}

	s.msgs = append(s.msgs, m)
	s.windows[token] = &window{createdAt: s.clk.Now()}

	close(s.pushed) // wake every parked long poll
	s.pushed = make(chan struct{})
	return token
}

// Sent returns every delivery attempt the gateway accepted, in arrival order.
// Calls rejected with an error body are not recorded; a reply the gateway
// swallowed on purpose is, with Dropped set.
func (s *Server) Sent() []SentRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SentRecord(nil), s.sent...)
}

func (s *Server) Typing() []TypingRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TypingRecord(nil), s.typingCh...)
}

// ExpireSession makes every POST answer -14 until the next ConfirmScan.
func (s *Server) ExpireSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionExpired = true
}

// FailNext arms a one-shot fault on path, which may be given with or without a
// leading slash. The next request to that path consumes it.
func (s *Server) FailNext(path string, mode FailMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[normalizePath(path)] = mode
}

func (s *Server) SetReplyLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.ReplyLimit = n
}
