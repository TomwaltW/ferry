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
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/im/wechat"
	"github.com/TomwaltW/ferry/internal/outbox"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/registry"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/TomwaltW/ferry/internal/store"
)

// dbName is the SQLite file inside FERRY_HOME.
const dbName = "ferry.db"

// assembly is everything the daemon runs on, wired in the order the spec
// prescribes: config, store, credentials, IM adapter, outbox, acl, approvals,
// registry, driver, commands, i18n, core.
type assembly struct {
	Config    config.Config
	Home      string
	Log       *slog.Logger
	Store     ports.Store
	Bundle    *i18n.Bundle
	Adapter   *wechat.Adapter
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
	st, err := openStore(home)
	if err != nil {
		return nil, err
	}
	a := &assembly{Config: cfg, Home: home, Log: log, Store: st, Bundle: i18n.Default()}

	creds, err := security.LoadCredentials(home, fsp.PlatformWeChat)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("load wechat credentials: %w", err)
	}
	a.Adapter, err = wechat.New(wechat.Options{
		Config: cfg.WeChat,
		Creds:  creds,
		KV:     st,
		Home:   home,
		Clock:  clock.Real(),
		Log:    log,
	})
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("start wechat adapter: %w", err)
	}

	a.Outbox = outbox.New(outbox.Options{
		Config: cfg.Outbox,
		IM:     a.Adapter,
		Store:  st,
		I18n:   a.Bundle,
		Lang:   langOf(st, cfg.Lang),
		Clock:  clock.Real(),
		Log:    log,
	})
	a.ACL = acl.New(cfg.WeChat, st, clock.Real())

	roots, err := cfg.NormalizedRoots()
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("workspace roots: %w", err)
	}
	a.Approvals = approval.New(approval.Options{
		Policy:      cfg.Policy,
		Roots:       roots,
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

	a.Daemon = core.New(core.Deps{
		Config:    cfg,
		Home:      home,
		IM:        a.Adapter,
		Drivers:   map[fsp.DriverKind]ports.Driver{fsp.DriverACP: acp.New(log)},
		Registry:  a.Registry,
		Store:     st,
		Outbox:    a.Outbox,
		ACL:       a.ACL,
		Approvals: a.Approvals,
		Commands:  command.New(),
		I18n:      a.Bundle,
		Clock:     clock.Real(),
		Log:       log,
	})
	return a, nil
}
