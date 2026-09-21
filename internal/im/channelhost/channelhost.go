package channelhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/driver/claudechannel"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/mcpmini"
	"github.com/TomwaltW/ferry/internal/ports"
)

// A server that exits is restarted on this cadence, and given up on after
// RestartLimit tries; the adapter then reports itself offline.
const (
	RestartInterval = 60 * time.Second
	RestartLimit    = 5
)

// clientName is how the adapter introduces itself in the MCP handshake.
const clientName = "ferry"

type Options struct {
	Config config.ChannelServerConfig
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}

// Adapter runs one third-party channel server and presents it as an IM entry
// point on platform channel-<name>.
type Adapter struct {
	cfg  config.ChannelServerConfig
	home string
	clk  clock.Clock
	log  *slog.Logger

	// noSender warns, once per adapter, that a message named no sender and
	// so is not the owner's.
	noSender sync.Once

	mu     sync.Mutex
	online bool
	detail string
	cli    *mcpmini.Client
}

func New(opts Options) (*Adapter, error) {
	cfg := opts.Config.WithDefaults()
	if cfg.Name == "" {
		return nil, errors.New("channelhost: a channel server needs a name")
	}
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("channelhost: %s has no command to run: %w", cfg.Name, fsp.ErrNotFound)
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.Owner == "" {
		log.Warn("channelhost: this channel server has no owner, so nobody on it is the owner; set owner to your sender ID, or, if the server reports no sender IDs and only you can write to it, to \"local\"", "name", cfg.Name)
	}
	return &Adapter{
		cfg:    cfg,
		home:   opts.Home,
		clk:    clk,
		log:    log,
		detail: "not started",
	}, nil
}

func (a *Adapter) Platform() fsp.Platform { return a.cfg.Platform() }

// Limits: a channel server takes a reply whenever one is ready.
func (a *Adapter) Limits() ports.DeliveryLimits {
	return ports.DeliveryLimits{NeedsWindow: false}
}

// Run keeps the server alive: it starts it, relays what it pushes, and brings
// it back when it exits, up to RestartLimit times. Once those are spent the
// adapter stays offline rather than ending, so that the daemon keeps reporting
// the entry point and its reason for being down.
func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	restarts := 0
	for {
		err := a.serve(ctx, out)
		if ctx.Err() != nil {
			return nil
		}
		if restarts >= RestartLimit {
			a.setOffline(fmt.Sprintf("the server exited %d times; giving up", restarts+1))
			a.log.Error("channelhost: giving up on the server", "name", a.cfg.Name, "restarts", restarts, "error", err)
			<-ctx.Done()
			return nil
		}
		restarts++
		a.setOffline(fmt.Sprintf("the server exited; restart %d of %d in %s", restarts, RestartLimit, RestartInterval))
		a.log.Warn("channelhost: the server exited; restarting", "name", a.cfg.Name, "restart", restarts, "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-a.clk.After(RestartInterval):
		}
	}
}

// serve runs the server once and returns when it goes away.
func (a *Adapter) serve(ctx context.Context, out chan<- fsp.Inbound) error {
	cli := mcpmini.NewClient(mcpmini.ClientOptions{
		Info:     mcpmini.Info{Name: clientName, Version: claudechannel.ServerVersion},
		Command:  a.cfg.Command,
		Env:      a.cfg.Env,
		OnNotify: a.inbound(out),
		Log:      a.log,
	})
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return err
	}

	a.mu.Lock()
	a.cli = cli
	a.online = true
	a.detail = ""
	a.mu.Unlock()

	defer func() {
		_ = cli.Close()
		a.mu.Lock()
		if a.cli == cli {
			a.cli = nil
		}
		a.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return nil
	case <-cli.Done():
		return cli.Err()
	}
}

// inbound turns the server's channel notifications into Ferry messages. Any
// other notification is not ours to interpret: this round does not forward
// approvals to a third-party server, so a permission request is only logged.
func (a *Adapter) inbound(out chan<- fsp.Inbound) mcpmini.NotifyFunc {
	return func(ctx context.Context, method string, params json.RawMessage) {
		if method != claudechannel.NotifyChannel {
			if method == claudechannel.NotifyPermissionRequest {
				a.log.Warn("channelhost: a third-party server asked to relay an approval, which this round does not do", "name", a.cfg.Name)
			}
			return
		}
		var p struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			a.log.Error("channelhost: unparsable channel notification", "name", a.cfg.Name, "error", err)
			return
		}

		sender := p.Meta[a.cfg.UserIDMeta]
		if sender == "" {
			// A message that names no sender is booked to local. It is the
			// owner's only where the config sets owner = "local"; anywhere
			// else it is a stranger's and has to pair like any other.
			if a.cfg.Owner != config.DefaultChannelOwner {
				a.noSender.Do(func() {
					a.log.Warn("channelhost: a message named no sender and is treated as a stranger's, not the owner's; if this server reports no sender IDs and only you can write to it, set owner = \"local\"", "name", a.cfg.Name, "owner", a.cfg.Owner)
				})
			}
			sender = config.DefaultChannelOwner
		}
		messageID := p.Meta[claudechannel.MetaMessageID]
		if messageID == "" {
			messageID = ids.New("cmsg")
		}
		chat := p.Meta[a.cfg.ChatIDMeta]

		msg := fsp.Inbound{
			Endpoint: fsp.EndpointID{
				Platform: a.cfg.Platform(),
				BotID:    a.cfg.Name,
				PeerID:   chat,
			},
			SenderID:   sender,
			MessageID:  messageID,
			Text:       p.Content,
			ReplyCtx:   chat,
			ReceivedAt: a.clk.Now(),
		}
		select {
		case out <- msg:
		case <-ctx.Done():
		}
	}
}

// Send calls the server's reply tool. A tool that refuses the call leaves the
// message rejected, which is what the outbox audits.
func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	a.mu.Lock()
	cli := a.cli
	a.mu.Unlock()
	if cli == nil {
		return fsp.DeliveryRejected, fmt.Errorf("channelhost: %s is offline: %w", a.cfg.Name, fsp.ErrNotFound)
	}

	chat := msg.Endpoint.PeerID
	if chat == "" {
		chat = replyCtx
	}
	// The reply tool carries text only, so a file travels as its path: the
	// alternative is dropping it without a word.
	text := msg.Text
	if text == "" && msg.FilePath != "" {
		text = msg.FilePath
	}

	res, err := cli.CallTool(ctx, a.cfg.ReplyTool, map[string]string{
		claudechannel.MetaChatID: chat,
		"text":                   text,
	})
	if err != nil {
		return fsp.DeliveryRejected, fmt.Errorf("channelhost: %s: call %s: %w", a.cfg.Name, a.cfg.ReplyTool, err)
	}
	if res.IsError {
		return fsp.DeliveryRejected, fmt.Errorf("channelhost: %s: %s refused the message: %s", a.cfg.Name, a.cfg.ReplyTool, toolErrorText(res))
	}
	return fsp.DeliveryAccepted, nil
}

// Typing: the channels protocol has no typing indicator.
func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	return nil
}

func (a *Adapter) Status() ports.AdapterStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ports.AdapterStatus{
		Online:  a.online,
		BotID:   a.cfg.Name,
		OwnerID: a.cfg.Owner,
		Detail:  a.detail,
	}
}

func (a *Adapter) setOffline(detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online = false
	a.detail = detail
}

func toolErrorText(res mcpmini.ToolResult) string {
	if len(res.Content) == 0 {
		return "no reason given"
	}
	return strconv.Quote(res.Content[0].Text)
}

var _ ports.IMAdapter = (*Adapter)(nil)
