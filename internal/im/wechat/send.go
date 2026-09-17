package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

const (
	// typingTicketTTL is how long a typing ticket is reused before getconfig is
	// asked for a fresh one.
	typingTicketTTL = 24 * time.Hour

	typingOn  = 1
	typingOff = 2
)

// errNoDedupeKey is returned for an outbound message the outbox forgot to key:
// without a client_id the gateway cannot recognise a retransmission.
var errNoDedupeKey = errors.New("wechat: outbound message has no dedupe key")

// Send delivers one message and reports what the gateway made of it. It never
// retries; the outbox owns that decision.
func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	if msg.Kind == fsp.OutFile {
		return fsp.DeliveryRejected, fsp.ErrUnsupported
	}
	if replyCtx == "" {
		return fsp.DeliveryRejected, fsp.ErrNoWindow
	}
	if msg.DedupeKey == "" {
		return fsp.DeliveryRejected, errNoDedupeKey
	}

	text := msg.Text
	if a.cfg.Render == renderPlainMode {
		text = renderPlain(text)
	}

	req := ilinkwire.SendMessageReq{
		Msg: &ilinkwire.Message{
			FromUserID:   a.creds.BotID,
			ToUserID:     msg.Endpoint.PeerID,
			ClientID:     msg.DedupeKey,
			ContextToken: replyCtx,
			MessageType:  ilinkwire.MsgTypeBot,
			MessageState: ilinkwire.StateFinish,
			ItemList: []ilinkwire.MessageItem{{
				Type:     ilinkwire.ItemText,
				TextItem: &ilinkwire.TextItem{Text: text},
			}},
		},
		BaseInfo: a.cli.baseInfo(),
	}

	err := a.cli.post(ctx, ilinkwire.PathSendMessage, req, nil)
	switch {
	case err == nil:
		a.log.Debug("wechat: message sent", "chars", len([]rune(text)))
		return fsp.DeliveryAccepted, nil
	case sessionExpired(err):
		a.setOffline("session expired")
		return fsp.DeliveryRejected, fsp.ErrSessionExpired
	case isAPIError(err):
		// The gateway answered and refused: retrying the same bytes changes
		// nothing.
		return fsp.DeliveryRejected, err
	default:
		// No answer, or one we could not read: the message may well have
		// landed, so it is the outbox that decides whether to try again.
		return fsp.DeliveryUnknown, err
	}
}

// Typing switches the typing indicator of one conversation.
func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	ticket, err := a.typingTicket(ctx, ep.PeerID, replyCtx)
	if err != nil {
		return err
	}

	status := typingOff
	if on {
		status = typingOn
	}
	req := ilinkwire.SendTypingReq{
		ILinkUserID:  ep.PeerID,
		TypingTicket: ticket,
		Status:       status,
		BaseInfo:     a.cli.baseInfo(),
	}
	if err := a.cli.post(ctx, ilinkwire.PathSendTyping, req, nil); err != nil {
		if sessionExpired(err) {
			a.setOffline("session expired")
			return fsp.ErrSessionExpired
		}
		return err
	}
	return nil
}

// cachedTicket is the KV payload behind one user's typing ticket.
type cachedTicket struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at_ms"`
}

// typingTicket returns the user's ticket, asking getconfig for one only when
// the cached ticket is missing or older than typingTicketTTL.
func (a *Adapter) typingTicket(ctx context.Context, user, replyCtx string) (string, error) {
	now := a.clk.Now()
	key := a.typingKey(user)
	if raw, ok := a.kvGet(ctx, key); ok {
		var cached cachedTicket
		if err := json.Unmarshal([]byte(raw), &cached); err == nil &&
			cached.Ticket != "" && now.UnixMilli() < cached.ExpiresAt {
			return cached.Ticket, nil
		}
	}

	req := ilinkwire.GetConfigReq{
		ILinkUserID:  user,
		ContextToken: replyCtx,
		BaseInfo:     a.cli.baseInfo(),
	}
	var resp ilinkwire.GetConfigResp
	if err := a.cli.post(ctx, ilinkwire.PathGetConfig, req, &resp); err != nil {
		if sessionExpired(err) {
			a.setOffline("session expired")
			return "", fsp.ErrSessionExpired
		}
		return "", err
	}
	if resp.TypingTicket == "" {
		return "", errors.New("wechat: getconfig returned no typing ticket")
	}

	entry, err := json.Marshal(cachedTicket{
		Ticket:    resp.TypingTicket,
		ExpiresAt: now.Add(typingTicketTTL).UnixMilli(),
	})
	if err == nil {
		a.kvSet(ctx, key, string(entry))
	}
	return resp.TypingTicket, nil
}

func isAPIError(err error) bool {
	var ae *apiError
	return errors.As(err, &ae)
}
