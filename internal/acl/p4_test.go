package acl

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/store"
)

// openStore opens a real store in a temporary database.
func openStore(t *testing.T) ports.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ferry.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// realTempDir is a temporary directory in the form Roots answers with.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return dir
}

// ---------- owner scope (spec-p4-b1 3.3) ----------

// The owner is the owner of the bot, so a chat that is not their own — a group,
// or a channel server whose chat_id differs from its user_id — lets them in
// without pairing, and nobody else gains anything from it.
func TestOwnerRecognisedOnEveryEndpoint(t *testing.T) {
	ctx := context.Background()
	a, _, _ := newACL(t, config.WeChatConfig{})
	if err := a.SetOwner(ctx, fsp.PlatformWeChat, "bot1", "ownerA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}

	room := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "room-1"}
	if !a.Allowed(room, "ownerA") {
		t.Error("Allowed(owner, another chat of the bot) = false, want true")
	}
	own := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "ownerA"}
	if !a.Allowed(own, "ownerA") {
		t.Error("Allowed(owner, own chat) = false, want true")
	}
	if a.Allowed(room, "someoneElse") {
		t.Error("Allowed(stranger, the owner's bot) = true, want false: owner scope widens nobody else")
	}
	if a.Allowed(own, "someoneElse") {
		t.Error("Allowed(stranger in the owner's chat) = true, want false")
	}
}

func TestOwnerNotRecognisedOnAnotherBot(t *testing.T) {
	ctx := context.Background()
	a := newFullACL(t, config.Config{})
	if err := a.SetOwner(ctx, fsp.PlatformWeChat, "bot1", "ownerA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}

	for _, ep := range []fsp.EndpointID{
		{Platform: fsp.PlatformWeChat, BotID: "bot2", PeerID: "ownerA"},
		{Platform: fsp.PlatformWeChat, BotID: "bot2", PeerID: "room-1"},
		{Platform: fsp.PlatformFeishu, BotID: "bot1", PeerID: "ownerA"},
	} {
		if a.Allowed(ep, "ownerA") {
			t.Errorf("Allowed(owner of wechat/bot1 at %s) = true, want false", ep)
		}
		if a.IsApprover(ep, "ownerA") {
			t.Errorf("IsApprover(owner of wechat/bot1 at %s) = true, want false", ep)
		}
	}
}

// "owner" in an approver list is the owner of the bot, in whatever chat they
// answer from; a member of that chat stays a member.
func TestOwnerKeywordMeansBotOwner(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	a := New(config.Config{}, st, clock.NewFake(base))
	if err := a.SetOwner(ctx, fsp.PlatformWeChat, "bot1", "ownerA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	room := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "room-1"}
	if err := st.SetRole(ctx, room, "memberB", RoleMember); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	if !a.IsApprover(room, "ownerA") {
		t.Error(`IsApprover(owner, another chat) = false, want true: "owner" means the bot owner`)
	}
	if a.IsApprover(room, "memberB") {
		t.Error("IsApprover(member) = true, want false")
	}

	// With a list that leaves "owner" out, the owner does not approve anywhere.
	b := New(config.Config{WeChat: config.WeChatConfig{Approvers: []string{"approverC"}}}, st, clock.NewFake(base))
	if b.IsApprover(room, "ownerA") {
		t.Error(`IsApprover(owner) = true with a list that omits "owner", want false`)
	}
	if !b.IsApprover(room, "approverC") {
		t.Error("IsApprover(named approver) = false, want true")
	}
}

// ---------- approvers per entry point ----------

func TestApproversForEntry(t *testing.T) {
	cfg := config.Config{
		WeChat: config.WeChatConfig{Approvers: []string{"wxUser"}},
		Feishu: config.FeishuConfig{Approvers: []string{"fsUser"}},
		ChannelServers: []config.ChannelServerConfig{
			{Name: "demo", Approvers: []string{"chUser"}},
			{Name: "plain"},
		},
	}
	a := newFullACL(t, cfg)

	cases := []struct {
		platform fsp.Platform
		sender   string
		want     bool
	}{
		{fsp.PlatformWeChat, "wxUser", true},
		{fsp.PlatformWeChat, "fsUser", false},
		{fsp.PlatformWeChat, "chUser", false},
		{fsp.PlatformFeishu, "fsUser", true},
		{fsp.PlatformFeishu, "wxUser", false},
		{"channel-demo", "chUser", true},
		{"channel-demo", "wxUser", false},
		{"channel-plain", "wxUser", false},
		{"channel-other", "wxUser", false},
		{fsp.PlatformFake, "wxUser", false},
	}
	for _, c := range cases {
		ep := fsp.EndpointID{Platform: c.platform, BotID: "bot1", PeerID: "roomA"}
		if got := a.IsApprover(ep, c.sender); got != c.want {
			t.Errorf("IsApprover(%s, %s) = %v, want %v", c.platform, c.sender, got, c.want)
		}
	}

	// Every entry point without a list of its own falls to ["owner"]: its owner
	// approves, on any of its chats.
	ctx := context.Background()
	for _, p := range []fsp.Platform{"channel-plain", "channel-other", fsp.PlatformFake} {
		if err := a.SetOwner(ctx, p, "bot1", "own-"+string(p)); err != nil {
			t.Fatalf("SetOwner: %v", err)
		}
		ep := fsp.EndpointID{Platform: p, BotID: "bot1", PeerID: "roomA"}
		if !a.IsApprover(ep, "own-"+string(p)) {
			t.Errorf(`IsApprover(owner of %s) = false, want true from the ["owner"] fallback`, p)
		}
	}
}

// A channel server's approvers are its own: the [wechat] list does not govern
// it, and when it names none its owner — local by default — approves.
func TestChannelApproversOwnList(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{
		WeChat: config.WeChatConfig{Approvers: []string{"owner", "wxUser"}},
		ChannelServers: []config.ChannelServerConfig{
			{Name: "listed", Approvers: []string{"chUser"}},
			{Name: "omitted"},
		},
	}
	a := newFullACL(t, cfg)

	listed := fsp.EndpointID{Platform: "channel-listed", BotID: "listed", PeerID: "room-1"}
	if err := a.SetOwner(ctx, listed.Platform, listed.BotID, config.DefaultChannelOwner); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if !a.IsApprover(listed, "chUser") {
		t.Error("IsApprover(channel-listed, chUser) = false, want true from its own list")
	}
	if a.IsApprover(listed, "wxUser") {
		t.Error("IsApprover(channel-listed, wxUser) = true, want false: the [wechat] list does not carry over")
	}
	if a.IsApprover(listed, config.DefaultChannelOwner) {
		t.Error(`IsApprover(channel-listed, owner) = true, want false: its list does not say "owner"`)
	}

	omitted := fsp.EndpointID{Platform: "channel-omitted", BotID: "omitted", PeerID: "room-1"}
	if err := a.SetOwner(ctx, omitted.Platform, omitted.BotID, config.DefaultChannelOwner); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if !a.IsApprover(omitted, config.DefaultChannelOwner) {
		t.Error(`IsApprover(channel-omitted, local) = false, want true: an omitted list is ["owner"]`)
	}
	if !a.Allowed(omitted, config.DefaultChannelOwner) {
		t.Error("Allowed(channel-omitted, local in room-1) = false, want true: the owner is not paired per chat")
	}
	if a.IsApprover(omitted, "wxUser") {
		t.Error("IsApprover(channel-omitted, wxUser) = true, want false")
	}
}

// ---------- Workspaces (spec-p4-b1 3.4) ----------

// workspacesFixture is a store with the owner recorded, plus the directories a
// [workspace] table points at.
type workspacesFixture struct {
	st        ports.Store
	shared    string
	usersRoot string
	carolDir  string
}

var wsOwnerEP = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "ownerA"}

func newWorkspacesFixture(t *testing.T) workspacesFixture {
	t.Helper()
	f := workspacesFixture{
		st:        openStore(t),
		shared:    realTempDir(t),
		usersRoot: realTempDir(t),
		carolDir:  realTempDir(t),
	}
	if err := New(config.Config{}, f.st, clock.NewFake(base)).SetOwner(context.Background(), wsOwnerEP.Platform, wsOwnerEP.BotID, wsOwnerEP.PeerID); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	return f
}

func roots(t *testing.T, w ports.Workspaces, ep fsp.EndpointID, sender string) []string {
	t.Helper()
	got, err := w.Roots(context.Background(), ep, sender)
	if err != nil {
		t.Fatalf("Roots(%s, %s): %v", ep, sender, err)
	}
	return got
}

// The owner always works in workspace.roots, in any chat, even when a
// [[workspace.users]] entry names them.
func TestRootsOwner(t *testing.T) {
	f := newWorkspacesFixture(t)
	w := NewWorkspaces(config.Config{Workspace: config.WorkspaceConfig{
		Roots:     []string{f.shared},
		UsersRoot: f.usersRoot,
		Users:     []config.WorkspaceUser{{Platform: "wechat", ID: "ownerA", Roots: []string{f.carolDir}}},
	}}, f.st)

	want := []string{f.shared}
	if got := roots(t, w, wsOwnerEP, "ownerA"); !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(owner) = %q, want %q", got, want)
	}
	room := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "room-1"}
	if got := roots(t, w, room, "ownerA"); !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(owner in another chat) = %q, want %q", got, want)
	}
	entries, err := os.ReadDir(f.usersRoot)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("users_root holds %d entries after asking for the owner, want 0", len(entries))
	}
}

// A [[workspace.users]] entry wins over users_root, and only on its platform.
func TestRootsExplicitUser(t *testing.T) {
	f := newWorkspacesFixture(t)
	w := NewWorkspaces(config.Config{Workspace: config.WorkspaceConfig{
		Roots:     []string{f.shared},
		UsersRoot: f.usersRoot,
		Users:     []config.WorkspaceUser{{Platform: "wechat", ID: "carol", Roots: []string{f.carolDir}}},
	}}, f.st)

	ep := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "carol"}
	if got, want := roots(t, w, ep, "carol"), []string{f.carolDir}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(carol) = %q, want %q", got, want)
	}

	// The same ID on Feishu is somebody else, who gets a users_root directory.
	fs := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: "app", PeerID: "carol"}
	want := []string{filepath.Join(f.usersRoot, config.UserDirName(fsp.PlatformFeishu, "carol"))}
	if got := roots(t, w, fs, "carol"); !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(feishu carol) = %q, want %q", got, want)
	}
}

func TestRootsUsersRootCreatesDir(t *testing.T) {
	f := newWorkspacesFixture(t)
	w := NewWorkspaces(config.Config{Workspace: config.WorkspaceConfig{
		Roots:     []string{f.shared},
		UsersRoot: f.usersRoot,
	}}, f.st)

	sender := "wxid_abc@im.wechat"
	ep := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: sender}
	dir := filepath.Join(f.usersRoot, config.UserDirName(fsp.PlatformWeChat, sender))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("user directory exists before Roots: %v", err)
	}

	if got, want := roots(t, w, ep, sender), []string{dir}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots = %q, want %q", got, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(user directory): %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	// Windows has no POSIX permission bits to check (spec-p4-b1 3.8).
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("user directory mode = %o, want 700", perm)
		}
	}

	// Asking again finds the directory and gives the same answer.
	if got, want := roots(t, w, ep, sender), []string{dir}; !reflect.DeepEqual(got, want) {
		t.Errorf("second Roots = %q, want %q", got, want)
	}

	// A directory that cannot be created is an error, never an empty answer.
	file := filepath.Join(realTempDir(t), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bad := NewWorkspaces(config.Config{Workspace: config.WorkspaceConfig{Roots: []string{f.shared}, UsersRoot: file}}, f.st)
	if got, err := bad.Roots(context.Background(), ep, sender); err == nil {
		t.Errorf("Roots under a users_root that is a file = %q, want an error", got)
	}
}

// Without users_root every paired user shares workspace.roots, as in P1–P3.
func TestRootsSharedWithoutUsersRoot(t *testing.T) {
	f := newWorkspacesFixture(t)
	w := NewWorkspaces(config.Config{Workspace: config.WorkspaceConfig{Roots: []string{f.shared}}}, f.st)

	ep := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "stranger"}
	if got, want := roots(t, w, ep, "stranger"), []string{f.shared}; !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(member) = %q, want the shared roots %q", got, want)
	}
}
