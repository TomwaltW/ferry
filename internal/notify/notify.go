package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The three reminders Ferry knows how to raise (spec-p2 3.1).
const (
	KindApprovalPending = "approval_pending"
	KindTurnFinished    = "turn_finished"
	KindIMOffline       = "im_offline"
)

// safeArgs are the only template arguments a reminder may carry. A wake-up
// travels over a third-party service, so nothing that could be a message body,
// a tool argument, a command or a path is allowed through: anything not named
// here is dropped before the text is rendered (spec-p2 3.1, B10).
var safeArgs = map[string]bool{"Slot": true, "N": true}

// sendRetries is how many extra attempts a failed reminder gets (spec-p2 3.12:
// at most one retry).
const sendRetries = 1

type Options struct {
	Config config.NotifyConfig
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}

// note is one rendered reminder on its way to the channels.
type note struct {
	Kind  string
	Title string
	Body  string
	At    time.Time
}

// channel is one way out of chat: ntfy, Bark, a webhook or e-mail.
type channel interface {
	name() string
	send(ctx context.Context, n note) error
}

type waker struct {
	cfg      config.NotifyConfig
	events   map[string]bool
	channels []channel
	i18n     ports.I18n
	lang     func(fsp.EndpointID) fsp.Lang
	clk      clock.Clock
	log      *slog.Logger

	mu   sync.Mutex
	last map[string]time.Time // endpoint|kind, for min_interval
}

// New builds a Waker over the channels notify.enabled names. An enabled channel
// that cannot be built at all is an error: a wake-up path that silently does
// nothing is worse than a configuration that refuses to start.
func New(opts Options) (ports.Waker, error) {
	w := &waker{
		cfg:    opts.Config,
		events: make(map[string]bool, len(opts.Config.Events)),
		i18n:   opts.I18n,
		lang:   opts.Lang,
		clk:    opts.Clock,
		log:    opts.Log,
		last:   make(map[string]time.Time),
	}
	if w.clk == nil {
		w.clk = clock.Real()
	}
	if w.log == nil {
		w.log = slog.New(slog.DiscardHandler)
	}
	if w.lang == nil {
		w.lang = func(fsp.EndpointID) fsp.Lang { return fsp.LangZH }
	}
	for _, e := range opts.Config.Events {
		w.events[e] = true
	}
	if len(opts.Config.Events) == 0 {
		// An empty list means the three defaults rather than silence.
		w.events[KindApprovalPending] = true
		w.events[KindTurnFinished] = true
		w.events[KindIMOffline] = true
	}
	for _, name := range opts.Config.Enabled {
		ch, err := build(opts.Config, name)
		if err != nil {
			return nil, err
		}
		w.channels = append(w.channels, ch)
	}
	return w, nil
}

// Wake renders the reminder for the endpoint's language and pushes it through
// every enabled channel. It never blocks on the result: a wake-up that fails is
// logged, not returned, because the caller is a send loop.
func (w *waker) Wake(ctx context.Context, ep fsp.EndpointID, kind string, args map[string]any) {
	if len(w.channels) == 0 || !w.events[kind] {
		return
	}
	now := w.clk.Now()
	if !w.allow(ep, kind, now) {
		w.log.Debug("notify: rate limited", "endpoint", ep.String(), "kind", kind)
		return
	}
	n := note{
		Kind:  kind,
		Title: w.text(ep, "notify."+kind+".title", args),
		Body:  w.text(ep, "notify."+kind+".body", args),
		At:    now,
	}
	for _, ch := range w.channels {
		w.deliver(ctx, ch, n)
	}
}

// allow enforces notify.min_interval per endpoint and kind.
func (w *waker) allow(ep fsp.EndpointID, kind string, now time.Time) bool {
	gap := w.cfg.MinInterval.Duration
	key := ep.String() + "|" + kind
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.last[key]; ok && gap > 0 && now.Sub(last) < gap {
		return false
	}
	w.last[key] = now
	return true
}

func (w *waker) deliver(ctx context.Context, ch channel, n note) {
	var err error
	for try := 0; try <= sendRetries; try++ {
		if err = ch.send(ctx, n); err == nil {
			return
		}
		if ctx.Err() != nil {
			break
		}
	}
	// The reminder itself is never logged: only which channel failed and why.
	w.log.Error("notify: send failed", "channel", ch.name(), "kind", n.Kind, "error", err)
}

// text renders a catalogue key with the arguments that survived the filter.
func (w *waker) text(ep fsp.EndpointID, key string, args map[string]any) string {
	safe := filterArgs(args)
	if w.i18n == nil {
		return key
	}
	return w.i18n.T(w.lang(ep), key, safe)
}

// filterArgs keeps only the template arguments on the allowlist.
func filterArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if safeArgs[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Test sends one reminder through a single channel, for `ferry notify test`.
// The channel does not have to be in notify.enabled: the point is to check the
// settings before turning it on.
func Test(ctx context.Context, cfg config.NotifyConfig, name string) error {
	ch, err := build(cfg, name)
	if err != nil {
		return err
	}
	return ch.send(ctx, note{
		Kind:  "test",
		Title: testTitle,
		Body:  testBody,
		At:    time.Now(),
	})
}

// The test reminder is diagnostic rather than conversational, so it carries no
// session detail and needs no catalogue entry.
const (
	testTitle = "Ferry"
	testBody  = "Ferry notify test"
)

func build(cfg config.NotifyConfig, name string) (channel, error) {
	switch name {
	case "ntfy":
		return newNtfy(cfg.Ntfy)
	case "bark":
		return newBark(cfg.Bark)
	case "webhook":
		return newWebhook(cfg.Webhook)
	case "email":
		return newEmail(cfg.Email)
	default:
		return nil, fmt.Errorf("notify: %q is not one of ntfy, bark, webhook, email", name)
	}
}

var _ ports.Waker = (*waker)(nil)
