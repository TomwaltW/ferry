// Package fakeilink is an in-process stand-in for the iLink bot gateway. It
// speaks the subset of the protocol Ferry uses and can be told to misbehave.
// P0-T1 implements the behaviour; this file only fixes the contract.
package fakeilink

import (
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
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

type Server struct {
	URL string
	// remaining fields are defined by P0-T1
}

func New(opts Options) *Server { panic("not implemented") }

func (s *Server) Close() { panic("not implemented") }

// ConfirmScan moves the current QR code to confirmed and issues a new bot token.
func (s *Server) ConfirmScan(ownerUserID string) { panic("not implemented") }

func (s *Server) BotToken() string { panic("not implemented") }

func (s *Server) BotID() string { panic("not implemented") }

func (s *Server) PushText(fromUserID, text string) (contextToken string) { panic("not implemented") }

func (s *Server) PushMessage(m ilinkwire.Message) (contextToken string) { panic("not implemented") }

// Sent returns every delivery attempt in arrival order.
func (s *Server) Sent() []SentRecord { panic("not implemented") }

func (s *Server) Typing() []TypingRecord { panic("not implemented") }

// ExpireSession makes every POST answer -14 until the next ConfirmScan.
func (s *Server) ExpireSession() { panic("not implemented") }

func (s *Server) FailNext(path string, mode FailMode) { panic("not implemented") }

func (s *Server) SetReplyLimit(n int) { panic("not implemented") }
