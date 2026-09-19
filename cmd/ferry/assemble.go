package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/TomwaltW/ferry/internal/acl"
	"github.com/TomwaltW/ferry/internal/approval"
	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/command"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/core"
	"github.com/TomwaltW/ferry/internal/driver/acp"
	"github.com/TomwaltW/ferry/internal/driver/server"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/im/channelhost"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/im/wechat"
	"github.com/TomwaltW/ferry/internal/notify"
	"github.com/TomwaltW/ferry/internal/outbox"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/registry"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/TomwaltW/ferry/internal/store"
)

// dbName is the SQLite file inside FERRY_HOME.
const dbName = "ferry.db"

// assembly is everything the daemon runs on, wired in the order the spec
// prescribes: config, store, credentials, IM adapters, wake-up channels,
// outbox, acl, approvals, registry, drivers, commands, i18n, core.
type assembly struct {
	Config    config.Config
	Home      string
	Log       *slog.Logger
	Store     ports.Store
	Bundle    *i18n.Bundle
	IMs       []ports.IMAdapter // one per enabled platform, in start order
	Outbox    ports.Outbox
	ACL       ports.ACL
	Approvals ports.Approvals
	Registry  ports.Registry
	Daemon    *core.Daemon
}

func (a *assembly) Close() {
	if a.Store != nil {
		_ = a.Store.Close()
	}
}

// langOf resolves the bot language of an endpoint: the per-endpoint KV entry
// first, then the configured default.
func langOf(st ports.Store, def fsp.Lang) func(fsp.EndpointID) fsp.Lang {
	return func(ep fsp.EndpointID) fsp.Lang {
		v, err := st.GetKV(context.Background(), "lang:"+ep.String())
		if err != nil || v == "" {
			return def
		}
		return fsp.Lang(v)
	}
}

// openStore opens the database, reporting the path along with any failure so
// the operator can act on it.
func openStore(home string) (ports.Store, error) {
	path := filepath.Join(home, dbName)
	st, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return st, nil
}

func assemble(home string, log *slog.Logger) (*assembly, error) {
	cfg, err := config.Load(config.Path(home))
	if err != nil {
		return nil, err
	}
	// config.Validate predates the P3 tables; their checks live apart.
	if err := cfg.ValidateEntries(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", config.Path(home), err)
	}
	st, err := openStore(home)
	if err != nil {
		return nil, err
	}
	a := &assembly{Config: cfg, Home: home, Log: log, Store: st, Bundle: i18n.Default()}

	a.IMs, err = buildIMs(cfg, home, st, a.Bundle, log)
	if err != nil {
		a.Close()
		return nil, err
	}
	byPlatform := make(map[fsp.Platform]ports.IMAdapter, len(a.IMs))
	for _, im := range a.IMs {
		byPlatform[im.Platform()] = im
	}

	// The wake-up channels reach the owner outside chat: an approval or an
	// urgent message that cannot get through, an entry point gone offline.
	waker, err := notify.New(notify.Options{
		Config: cfg.Notify,
		I18n:   a.Bundle,
		Lang:   langOf(st, cfg.Lang),
		Clock:  clock.Real(),
		Log:    log,
	})
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("notify: %w", err)
	}

	a.Outbox = outbox.New(outbox.Options{
		Config: cfg.Outbox,
		IMs:    byPlatform,
		Store:  st,
		I18n:   a.Bundle,
		Lang:   langOf(st, cfg.Lang),
		Clock:  clock.Real(),
		Log:    log,
		Waker:  waker,
		Home:   home,
	})
	a.ACL = acl.New(cfg, st, clock.Real())
	// Each paired user works in roots of their own once workspace.users_root
	// or [[workspace.users]] is set; otherwise everyone shares workspace.roots.
	workspaces := acl.NewWorkspaces(cfg, st)

	roots, err := cfg.NormalizedRoots()
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("workspace roots: %w", err)
	}
	a.Approvals = approval.New(approval.Options{
		Policy:      cfg.Policy,
		Roots:       roots,
		Workspaces:  workspaces,
		DefaultLang: cfg.Lang,
		Store:       st,
		Outbox:      a.Outbox,
		I18n:        a.Bundle,
		Clock:       clock.Real(),
		Log:         log,
	})

	a.Registry, err = registry.New(cfg.Registry, cfg.Agents, home, log)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("agent registry: %w", err)
	}

	drivers := map[fsp.DriverKind]ports.Driver{
		fsp.DriverACP:    acp.NewVersioned(log, acpVersions(cfg.Agents)),
		fsp.DriverServer: server.New(log),
	}
	a.Daemon = core.New(core.Deps{
		Config:     cfg,
		Home:       home,
		IMs:        a.IMs,
		Drivers:    drivers,
		Waker:      waker,
		Registry:   a.Registry,
		Store:      st,
		Outbox:     a.Outbox,
		ACL:        a.ACL,
		Approvals:  a.Approvals,
		Workspaces: workspaces,
		Commands:   command.New(),
		I18n:       a.Bundle,
		Clock:      clock.Real(),
		Log:        log,
	})
	return a, nil
}

// buildIMs creates one adapter per enabled platform, in the order
// config.EnabledPlatforms gives: WeChat, Feishu, then each channel server.
func buildIMs(cfg config.Config, home string, st ports.Store, bundle ports.I18n, log *slog.Logger) ([]ports.IMAdapter, error) {
	var ims []ports.IMAdapter
	if cfg.WeChat.Enabled {
		creds, err := security.LoadCredentials(home, fsp.PlatformWeChat)
		if err != nil {
			return nil, fmt.Errorf("load wechat credentials: %w", err)
		}
		wx, err := wechat.New(wechat.Options{
			Config: cfg.WeChat,
			Creds:  creds,
			KV:     st,
			Home:   home,
			Clock:  clock.Real(),
			Log:    log,
		})
		if err != nil {
			return nil, fmt.Errorf("start wechat adapter: %w", err)
		}
		ims = append(ims, wx)
	}
	if cfg.Feishu.Enabled {
		// ferry login feishu keeps the app secret in the credential store,
		// never in the configuration file.
		creds, err := security.LoadCredentials(home, fsp.PlatformFeishu)
		if err != nil {
			return nil, fmt.Errorf("load feishu credentials (run ferry login feishu): %w", err)
		}
		fs, err := feishu.New(feishu.Options{
			Config: cfg.Feishu,
			Secret: creds.BotToken,
			Home:   home,
			Clock:  clock.Real(),
			Log:    log,
			// Approvals go out as cards with buttons; without I18n they
			// would fall back to plain text.
			I18n: bundle,
			Lang: langOf(st, cfg.Lang),
		})
		if err != nil {
			return nil, fmt.Errorf("start feishu adapter: %w", err)
		}
		ims = append(ims, fs)
	}
	for _, sc := range cfg.ChannelServers {
		ch, err := channelhost.New(channelhost.Options{
			Config: sc,
			Home:   home,
			Clock:  clock.Real(),
			Log:    log,
		})
		if err != nil {
			return nil, fmt.Errorf("start channel server %s: %w", sc.Name, err)
		}
		ims = append(ims, ch)
	}
	if len(ims) == 0 {
		return nil, fmt.Errorf("no entry point is enabled: %w", fsp.ErrNotFound)
	}
	return ims, nil
}

// acpVersions answers, per agent, the protocol version its configuration asks
// for; agents the configuration does not name speak v1.
func acpVersions(agents []config.AgentConfig) acp.Versions {
	byName := make(map[string]string, len(agents))
	for _, a := range agents {
		byName[a.Name] = a.ACPVersionOrDefault()
	}
	return func(agent string) string {
		if v, ok := byName[agent]; ok {
			return v
		}
		return config.DefaultACPVersion
	}
}
