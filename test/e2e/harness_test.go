// Package e2e drives the real daemon against the fake iLink gateway and the
// fake ACP agent. Nothing here talks to WeChat or to a real agent.
package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/acl"
	"github.com/TomwaltW/ferry/internal/approval"
	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/command"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/core"
	"github.com/TomwaltW/ferry/internal/driver/acp"
	"github.com/TomwaltW/ferry/internal/driver/server"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/handoff"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/im/channelhost"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/im/feishu/feishutest"
	"github.com/TomwaltW/ferry/internal/im/wechat"
	"github.com/TomwaltW/ferry/internal/notify"
	"github.com/TomwaltW/ferry/internal/outbox"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/registry"
	"github.com/TomwaltW/ferry/internal/store"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"

	_ "modernc.org/sqlite"
)

const (
	ownerID   = "owner-wxid"
	stranger  = "stranger-wxid"
	settle    = 3 * time.Second // generous: every wait is condition based, not a sleep
	agentName = "fake"
)

// agentBin is the fake ACP agent, ferryBin the ferry command itself (ferry
// proxy, ferry channel and ferry run are driven as the subprocesses they are in
// real use), echoChannelBin the third-party channel server of B7 and tapBin
// the wire recorder of B8. All are built once for the whole package.
var agentBin, ferryBin, echoChannelBin, tapBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ferry-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: temp dir:", err)
		os.Exit(1)
	}
	name := "ferry-fakeagent"
	if runtime.GOOS == "windows" {
		name += ".exe" // Windows will not exec a file without the extension
	}
	agentBin = filepath.Join(dir, name)
	if _, err := os.Stat("../../go.mod"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: expected to run from the repository:", err)
		os.Exit(1)
	}
	suffix := strings.TrimPrefix(name, "ferry-fakeagent")
	ferryBin = filepath.Join(dir, "ferry"+suffix)
	echoChannelBin = filepath.Join(dir, "echo-channel"+suffix)
	tapBin = filepath.Join(dir, "acptap"+suffix)
	for _, b := range []struct{ out, pkg string }{
		{agentBin, "./cmd/ferry-fakeagent"},
		{ferryBin, "./cmd/ferry"},
		{echoChannelBin, "./test/e2e/testdata/echo-channel"},
		{tapBin, "./test/e2e/testdata/acptap"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: building %s: %v\n%s", b.pkg, err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// rig is one running daemon plus the fake gateway it talks to.
type rig struct {
	t      *testing.T
	gw     *fakeilink.Server
	home   string
	ws     string
	cfg    config.Config
	bundle *i18n.Bundle

	// fs is the fake Feishu client, set by withFeishu. It outlives restarts,
	// as the real app would.
	fs *feishutest.Client

	ep      fsp.EndpointID
	store   ports.Store
	adapter *wechat.Adapter
	aclmod  ports.ACL
	driver  ports.Driver
	daemon  *core.Daemon
	cancel  context.CancelFunc
	done    chan struct{}

	// patience is how long waitFor waits; settle unless a test needs longer.
	patience time.Duration

	// handoff wires terminal handoff (spec-p2) into the daemon; set by
	// withHandoff. Off by default so /ls in the other tests lists only the
	// sessions Ferry owns.
	handoff bool
}

// rigOption adjusts the configuration before the daemon first starts.
type rigOption func(*rig)

func newRig(t *testing.T, opts ...rigOption) *rig {
	t.Helper()
	gw := fakeilink.New(fakeilink.Options{LongPoll: 200 * time.Millisecond})
	t.Cleanup(gw.Close)
	gw.ConfirmScan(ownerID)

	home := t.TempDir()
	// The core resolves every workspace path (filepath.EvalSymlinks), so the rig
	// has to hold the resolved spelling too. On macOS the unresolved form is a
	// substring of the resolved one (/var/... inside /private/var/...), which is
	// why comparing the raw path passed there by accident; on Windows the 8.3
	// short name (RUNNER~1) shares no substring with the long one and it fails.
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	ws := filepath.Join(home, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "demo"), 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "demo", "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("workspace file: %v", err)
	}

	cfg := config.Default()
	cfg.Lang = fsp.LangZH
	cfg.DefaultAgent = agentName
	cfg.Workspace.Roots = []string{ws}
	cfg.Agents = []config.AgentConfig{{
		Name:    agentName,
		Command: []string{agentBin},
		Env:     map[string]string{"FAKEAGENT_HOME": filepath.Join(home, "agent")},
	}}
	cfg.WeChat.BaseURL = gw.URL
	// Without this the adapter uploads and fetches media against the
	// production WeChat CDN (internal/im/wechat/cdn.go:52). Every rig gets
	// the fake gateway's own CDN so no test can reach the real one.
	cfg.WeChat.CDNBaseURL = gw.CDNBaseURL()
	cfg.Outbox.MergeInterval = config.Duration{Duration: 200 * time.Millisecond}
	cfg.Policy.ApprovalTimeout = config.Duration{Duration: 2 * time.Second}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("e2e config invalid: %v", err)
	}

	r := &rig{t: t, gw: gw, home: home, ws: ws, cfg: cfg, bundle: i18n.Default(), patience: settle}
	r.ep = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: gw.BotID(), PeerID: ownerID}
	for _, o := range opts {
		o(r)
	}
	if err := r.cfg.Validate(); err != nil {
		t.Fatalf("e2e config invalid: %v", err)
	}
	if err := r.cfg.ValidateEntries(); err != nil {
		t.Fatalf("e2e config entries invalid: %v", err)
	}
	// ferry proxy, ferry channel and ferry run read the configuration from
	// FERRY_HOME, exactly as they do next to a real daemon.
	if err := config.Save(config.Path(home), r.cfg); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
	r.start()
	t.Cleanup(r.stop)
	return r
}

// start assembles the real modules and runs the daemon, exactly as the
// `ferry daemon` command does.
func (r *rig) start() {
	t := r.t
	t.Helper()

	st, err := store.Open(filepath.Join(r.home, "ferry.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	r.store = st

	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	creds := ports.Credentials{
		BotToken: r.gw.BotToken(),
		BotID:    r.gw.BotID(),
		OwnerID:  ownerID,
		BaseURL:  r.gw.URL,
	}
	adapter, err := wechat.New(wechat.Options{
		Config: r.cfg.WeChat, Creds: creds, KV: st, Home: r.home, Clock: clock.Real(), Log: log,
	})
	if err != nil {
		t.Fatalf("wechat adapter: %v", err)
	}
	r.adapter = adapter
	// The same entry points, in the same order, as cmd/ferry's buildIMs.
	ims := []ports.IMAdapter{adapter}
	if r.cfg.Feishu.Enabled {
		fs, err := feishu.NewWithClient(feishu.Options{
			Config: r.cfg.Feishu, Secret: "e2e-secret", Home: r.home, Clock: clock.Real(), Log: log,
			I18n: r.bundle, Lang: func(fsp.EndpointID) fsp.Lang { return r.cfg.Lang },
		}, r.fs)
		if err != nil {
			t.Fatalf("feishu adapter: %v", err)
		}
		ims = append(ims, fs)
	}
	for _, sc := range r.cfg.ChannelServers {
		ch, err := channelhost.New(channelhost.Options{Config: sc, Home: r.home, Clock: clock.Real(), Log: log})
		if err != nil {
			t.Fatalf("channel server %s: %v", sc.Name, err)
		}
		ims = append(ims, ch)
	}
	byPlatform := make(map[fsp.Platform]ports.IMAdapter, len(ims))
	for _, im := range ims {
		byPlatform[im.Platform()] = im
	}
	// The waker is built unconditionally, as production does: with no
	// notify.enabled it holds no channels and every Wake is a no-op, so the
	// rest of the suite is unaffected.
	waker, err := notify.New(notify.Options{
		Config: r.cfg.Notify, I18n: r.bundle,
		Lang:  func(fsp.EndpointID) fsp.Lang { return r.cfg.Lang },
		Clock: clock.Real(), Log: log,
	})
	if err != nil {
		t.Fatalf("waker: %v", err)
	}
	ob := outbox.New(outbox.Options{
		Config: r.cfg.Outbox, IMs: byPlatform, Store: st, I18n: r.bundle,
		Lang:  func(fsp.EndpointID) fsp.Lang { return r.cfg.Lang },
		Clock: clock.Real(), Log: log,
		Waker: waker,
		// Production passes Home (cmd/ferry/assemble.go); without it
		// fileWanted is always false and long_output = file is inert.
		Home: r.home,
	})
	roots, err := r.cfg.NormalizedRoots()
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	workspaces := acl.NewWorkspaces(r.cfg, st)
	ap := approval.New(approval.Options{
		Policy: r.cfg.Policy, Roots: roots, Workspaces: workspaces, DefaultLang: r.cfg.Lang,
		Store: st, Outbox: ob, I18n: r.bundle, Clock: clock.Real(), Log: log,
	})
	reg, err := registry.New(r.cfg.Registry, r.cfg.Agents, r.home, log)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	versions := make(map[string]string, len(r.cfg.Agents))
	for _, a := range r.cfg.Agents {
		versions[a.Name] = a.ACPVersionOrDefault()
	}
	r.driver = acp.NewVersioned(log, func(agent string) string { return versions[agent] })
	r.aclmod = acl.New(r.cfg, st, clock.Real())
	// The same driver table as cmd/ferry's assemble: an agent with a server
	// block is driven over HTTP, everything else speaks ACP.
	drivers := map[fsp.DriverKind]ports.Driver{
		fsp.DriverACP:    r.driver,
		fsp.DriverServer: server.New(log),
	}
	var ho ports.Handoff
	if r.handoff {
		ho = handoff.New(handoff.Options{
			Roots: roots, Agents: r.cfg.Agents, Profiles: r.cfg.Handoff.Profiles,
			Registry: reg, Drivers: drivers, Store: st, Clock: clock.Real(), Log: log,
		})
	}
	r.daemon = core.New(core.Deps{
		Config: r.cfg, Home: r.home, IMs: ims,
		Drivers:  drivers,
		Registry: reg, Store: st, Outbox: ob, ACL: r.aclmod, Handoff: ho,
		Approvals: ap, Workspaces: workspaces, Waker: waker,
		Commands: command.New(), I18n: r.bundle, Clock: clock.Real(), Log: log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := control.ServeWith(ctx, control.ServeOptions{
			Home: r.home, Handler: r.daemon.ControlHandler(), Log: log,
		}); err != nil {
			t.Errorf("control.ServeWith: %v", err)
		}
	}()
	go func() {
		defer close(r.done)
		if err := r.daemon.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("daemon.Run: %v", err)
		}
		<-served
	}()
	// The subprocess commands find the daemon through control.json.
	r.waitFor("the control endpoint", func() bool {
		_, err := os.Stat(filepath.Join(r.home, control.SocketName))
		return err == nil
	})
	// The daemon is up once it reports a status; that also means the IM loop started.
	r.waitFor("daemon to come up", func() bool { return r.daemon.Status().BotID != "" || r.daemon.Status().UpdatedAt.IsZero() })
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.t.Error("daemon did not stop within 10s")
	}
	if r.store != nil {
		_ = r.store.Close()
	}
	r.cancel = nil
}

// restart stops the daemon and brings a new one up on the same home, which is
// what a real restart looks like.
func (r *rig) restart() {
	r.t.Helper()
	r.stop()
	r.start()
}

// --- talking to the bot -------------------------------------------------

func (r *rig) send(text string) { r.gw.PushText(ownerID, text) }

// sendTok sends as the owner and returns the reply window that message opened,
// so a test can count exactly the replies belonging to it.
func (r *rig) sendTok(text string) string                { return r.gw.PushText(ownerID, text) }
func (r *rig) sendFrom(user, text string)                { r.gw.PushText(user, text) }
func (r *rig) tr(key string, args map[string]any) string { return r.bundle.T(r.cfg.Lang, key, args) }

// sentTo returns the texts delivered to one user, in order.
func (r *rig) sentTo(user string) []string {
	var out []string
	for _, s := range r.gw.Sent() {
		if s.ToUserID == user && !s.Duplicate {
			out = append(out, s.Text)
		}
	}
	return out
}

func (r *rig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(r.patience)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s\nmessages so far:\n%s", what, strings.Join(r.sentTo(ownerID), "\n---\n"))
}

// mark records how many messages a user has received, so a later wait can
// ignore everything already on screen.
func (r *rig) mark(user string) int { return len(r.sentTo(user)) }

func (r *rig) markOwner() int { return r.mark(ownerID) }

// waitTextFrom waits until a message at or after index from contains want.
func (r *rig) waitTextFrom(user string, from int, want string) string {
	r.t.Helper()
	var hit string
	r.waitFor(fmt.Sprintf("a message to %s after #%d containing %q", user, from, want), func() bool {
		msgs := r.sentTo(user)
		for i := from; i < len(msgs); i++ {
			if strings.Contains(msgs[i], want) {
				hit = msgs[i]
				return true
			}
		}
		return false
	})
	return hit
}

// waitText waits until some message to user contains want, and returns it.
func (r *rig) waitText(user, want string) string { return r.waitTextFrom(user, 0, want) }

func (r *rig) waitOwnerText(want string) string { return r.waitText(ownerID, want) }

func (r *rig) waitOwnerTextFrom(from int, want string) string {
	return r.waitTextFrom(ownerID, from, want)
}

// refuteFrom fails if want shows up among the messages after index from,
// within a short grace period.
func (r *rig) refuteFrom(user string, from int, want, why string) {
	r.t.Helper()
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		msgs := r.sentTo(user)
		for i := from; i < len(msgs); i++ {
			if strings.Contains(msgs[i], want) {
				r.t.Fatalf("%s: did not expect %q, got:\n%s", why, want, msgs[i])
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *rig) refute(user, want, why string) { r.refuteFrom(user, 0, want, why) }

// newSession opens a managed session on <ws>/demo and waits for the receipt.
func (r *rig) newSession() {
	r.t.Helper()
	r.send("/new " + agentName + " " + filepath.Join(r.ws, "demo"))
	r.waitOwnerText("会话 1")
}

// approvalID matches the ID inside the reply instructions of an approval
// request ("如「同意 <id>」"). Matching the leading "审批 <id>" instead would
// also match approval.decided, which shares that prefix.
var approvalID = regexp.MustCompile(`如「同意 ([a-km-z]{5})」`)

// approvalMark is the part of approval.request that no other message carries.
// It is checked against the catalog so a wording change fails loudly here
// rather than silently matching the wrong message.
func (r *rig) approvalMark() string {
	r.t.Helper()
	const mark = "回复「拒绝」拒绝"
	req := r.tr("approval.request", map[string]any{
		"ID": "aaaaa", "Tool": "t", "Description": "d", "Preview": "p", "AlwaysHint": "",
	})
	if !strings.Contains(req, mark) {
		r.t.Fatalf("approval.request no longer contains %q, update the e2e marker:\n%s", mark, req)
	}
	if decided := r.tr("approval.decided", map[string]any{"ID": "aaaaa", "Decision": "x"}); strings.Contains(decided, mark) {
		r.t.Fatalf("approval.decided also contains %q, so it no longer tells the two apart", mark)
	}
	if hint := r.tr("approval.need_id", map[string]any{"N": 1, "ID": "aaaaa", "List": "l"}); strings.Contains(hint, mark) {
		r.t.Fatalf("approval.need_id also contains %q, so it no longer tells the two apart", mark)
	}
	return mark
}

// waitApproval waits for the next approval request after from and returns its ID.
func (r *rig) waitApproval(from int) (string, string) {
	r.t.Helper()
	msg := r.waitOwnerTextFrom(from, r.approvalMark())
	return msg, r.approvalIDFrom(msg)
}

// approvalIDFrom pulls the 5 character approval ID out of an approval request.
func (r *rig) approvalIDFrom(msg string) string {
	r.t.Helper()
	m := approvalID.FindStringSubmatch(msg)
	if m == nil {
		r.t.Fatalf("no 5 character approval ID in:\n%s", msg)
	}
	return m[1]
}

// auditKinds reads the audit table directly; ports.Store has no read side.
func (r *rig) auditKinds() []string {
	r.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(r.home, "ferry.db"))
	if err != nil {
		r.t.Fatalf("open audit db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind FROM audit ORDER BY id`)
	if err != nil {
		r.t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			r.t.Fatalf("scan audit: %v", err)
		}
		kinds = append(kinds, k)
	}
	return kinds
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// countWindow counts the replies delivered inside one reply window.
func countWindow(r *rig, token string) int {
	n := 0
	for _, s := range r.gw.Sent() {
		if s.ContextToken == token && !s.Duplicate {
			n++
		}
	}
	return n
}

// quiet reports whether the window has seen no new delivery for d.
func (r *rig) quiet(token string, d time.Duration) bool {
	last := time.Time{}
	for _, s := range r.gw.Sent() {
		if s.ContextToken == token && s.At.After(last) {
			last = s.At
		}
	}
	return !last.IsZero() && time.Since(last) > d
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

var sixDigits = regexp.MustCompile(`\b(\d{6})\b`)

// regexpDigits pulls the 6 digit pairing code out of a message.
func regexpDigits(t *testing.T, msg string) string {
	t.Helper()
	m := sixDigits.FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("no 6 digit pairing code in:\n%s", msg)
	}
	return m[1]
}
