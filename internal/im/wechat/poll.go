package wechat

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

const (
	// defaultPollTimeout is the HTTP deadline of the first long poll, before
	// the gateway has told us how long it intends to hold a request.
	defaultPollTimeout = 40 * time.Second
	// pollTimeoutSlack is added to the timeout the gateway suggests.
	pollTimeoutSlack = 5 * time.Second

	minBackoff = 1 * time.Second
	maxBackoff = 60 * time.Second
)

// Run long polls getupdates until ctx ends. Every batch is delivered before its
// cursor is stored, so a crash replays the batch rather than losing it; the
// core drops what it has already seen.
func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	if a.creds.BotToken == "" {
		a.setOffline("not logged in")
		return fsp.ErrSessionExpired
	}

	cursor, _ := a.kvGet(ctx, a.cursorKey())
	timeout := defaultPollTimeout
	var backoff time.Duration

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		resp, err := a.getUpdates(ctx, cursor, timeout)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			switch {
			case sessionExpired(err):
				a.setOffline("session expired")
				a.log.Warn("wechat: bot session expired, scan a new qr code")
				return fsp.ErrSessionExpired
			case timedOut(err):
				// The gateway held the request and had nothing to say.
				backoff = 0
				continue
			default:
				backoff = nextBackoff(backoff)
				a.log.Warn("wechat: getupdates failed", "error", err, "retry_in", backoff)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-a.clk.After(backoff):
				}
				continue
			}
		}

		backoff = 0
		a.setOnline()
		if resp.LongPollingTimeoutMs > 0 {
			timeout = time.Duration(resp.LongPollingTimeoutMs)*time.Millisecond + pollTimeoutSlack
		}

		for _, m := range resp.Msgs {
			in, ok := a.toInbound(ctx, m)
			if !ok {
				continue
			}
			select {
			case out <- in:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != cursor {
			cursor = resp.GetUpdatesBuf
			a.kvSet(ctx, a.cursorKey(), cursor)
		}
	}
}

func (a *Adapter) getUpdates(ctx context.Context, cursor string, timeout time.Duration) (ilinkwire.GetUpdatesResp, error) {
	// The HTTP deadline is wall clock by nature; every wait Ferry decides on
	// goes through the injected clock instead.
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := ilinkwire.GetUpdatesReq{GetUpdatesBuf: cursor, BaseInfo: a.cli.baseInfo()}
	var resp ilinkwire.GetUpdatesResp
	err := a.cli.post(reqCtx, ilinkwire.PathGetUpdates, req, &resp)
	return resp, err
}

// nextBackoff doubles the wait from one second up to a minute.
func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return minBackoff
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

// toInbound converts a gateway message. Messages the bot itself sent and group
// messages are dropped, which is what the false return means.
//
// Media is downloaded here, inside the poll loop: the gateway holds the next
// request for half a minute anyway, and a message must not reach the core
// before the files it mentions exist on disk.
func (a *Adapter) toInbound(ctx context.Context, m ilinkwire.Message) (fsp.Inbound, bool) {
	if m.MessageType == ilinkwire.MsgTypeBot || m.GroupID != "" {
		return fsp.Inbound{}, false
	}

	id := m.ClientID
	if m.MessageID != 0 {
		id = strconv.FormatInt(m.MessageID, 10)
	}
	ep := a.endpoint(m.FromUserID)

	var texts []string
	var atts []fsp.Attachment
	for _, it := range m.ItemList {
		switch it.Type {
		case ilinkwire.ItemText:
			if it.TextItem != nil && it.TextItem.Text != "" {
				texts = append(texts, it.TextItem.Text)
			}
		case ilinkwire.ItemVoice:
			// WeChat transcribes voice itself; the words join the body and the
			// recording stays an attachment.
			if it.VoiceItem != nil && it.VoiceItem.Text != "" {
				texts = append(texts, it.VoiceItem.Text)
			}
			atts = append(atts, a.attach(ctx, ep, id, it))
		case ilinkwire.ItemImage, ilinkwire.ItemFile:
			atts = append(atts, a.attach(ctx, ep, id, it))
		case ilinkwire.ItemVideo:
			// Video is described but never fetched this round.
			atts = append(atts, fileAttachment("video", it))
		}
	}

	return fsp.Inbound{
		Endpoint:    ep,
		SenderID:    m.FromUserID,
		MessageID:   id,
		Text:        strings.Join(texts, "\n"),
		Attachments: atts,
		ReplyCtx:    m.ContextToken,
		ReceivedAt:  time.UnixMilli(m.CreateTimeMs),
	}, true
}

// fileAttachment describes a video item from its metadata alone. Video is out
// of scope this round, so Path stays empty.
func fileAttachment(kind string, it ilinkwire.MessageItem) fsp.Attachment {
	att := fsp.Attachment{Kind: kind, Name: it.MsgID}
	if it.FileItem == nil {
		return att
	}
	if it.FileItem.FileName != "" {
		att.Name = it.FileItem.FileName
	}
	if size, err := strconv.ParseInt(it.FileItem.Len, 10, 64); err == nil {
		att.Size = size
	}
	return att
}
