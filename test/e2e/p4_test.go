package e2e

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/im/feishu/feishutest"
	"github.com/TomwaltW/ferry/internal/ports"
)

// --- multi-user isolation -------------------------------------------------

// withUsersRoot turns on per-user workspaces under <home>/users. The directory
// has to exist before the configuration is validated.
func withUsersRoot() rigOption {
	return func(r *rig) {
		dir := filepath.Join(r.home, "users")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.t.Fatalf("users root: %v", err)
		}
		r.cfg.Workspace.UsersRoot = dir
	}
}

// B1: with workspace.users_root set, a paired stranger works in a directory of
// their own, cannot open a session in the owner's workspace, and neither side
// sees the other's sessions; the stranger sees no terminal sessions either.
func TestMultiUserIsolation(t *testing.T) {
	r := newRig(t, withUsersRoot(), withHandoff())
	// /ls starts the agent to discover terminal sessions, which takes longer
	// than the usual settle time on a loaded machine.
	r.patience = subprocWait
	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })

	demo := filepath.Join(r.ws, "demo")
	userDir := filepath.Join(r.cfg.Workspace.UsersRoot, config.UserDirName(fsp.PlatformWeChat, stranger))
	// A terminal session in the owner's workspace: the owner's /ls lists it,
	// which is what makes its absence from the stranger's /ls mean something.
	const title = "owner-terminal"
	r.writeTerminalSession("term-owner", demo, title)

	r.sendFrom(stranger, "hello")
	code := regexpDigits(t, r.waitText(stranger, "配对码"))
	r.send("/pair " + code)
	r.waitText(stranger, r.tr("pair.confirmed_workspace", map[string]any{"Dir": userDir}))

	// /new without a directory lands in the stranger's own directory.
	mark := r.mark(stranger)
	r.sendFrom(stranger, "/new "+agentName)
	r.waitTextFrom(stranger, mark, r.tr("session.created", map[string]any{"Slot": 1, "Agent": agentName, "Cwd": userDir}))
	if fi, err := os.Stat(userDir); err != nil || !fi.IsDir() {
		t.Fatalf("the stranger's directory %s was not created: %v", userDir, err)
	}

	// The owner's workspace is off limits.
	mark = r.mark(stranger)
	r.sendFrom(stranger, "/new "+agentName+" "+demo)
	r.waitTextFrom(stranger, mark, r.tr("workspace.denied", map[string]any{"Dir": demo}))

	r.newSession()

	// The owner lists their own session and the terminal one, not the stranger's.
	mark = r.markOwner()
	r.send("/ls")
	ownerList := r.waitOwnerTextFrom(mark, title)
	if !strings.Contains(ownerList, demo) || strings.Contains(ownerList, userDir) {
		t.Errorf("the owner's /ls should list %s and not %s:\n%s", demo, userDir, ownerList)
	}

	// The stranger lists their own session only: no owner session, no terminal.
	mark = r.mark(stranger)
	r.sendFrom(stranger, "/ls")
	strangerList := r.waitTextFrom(stranger, mark, r.tr("session.list_header", nil))
	if !strings.Contains(strangerList, userDir) {
		t.Errorf("the stranger's /ls should list %s:\n%s", userDir, strangerList)
	}
	if strings.Contains(strangerList, demo) || strings.Contains(strangerList, title) {
		t.Errorf("the stranger's /ls shows the owner's workspace:\n%s", strangerList)
	}
	r.refuteFrom(stranger, mark, title, "a terminal session outside the stranger's roots")
}

// --- owner in any chat ----------------------------------------------------

// B2: the owner a channel server reports is the owner in every chat of that
// server, not only in a chat whose ID happens to equal the owner's.
func TestOwnerAnyChat(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	const user = "u-owner"
	r := newRig(t, func(r *rig) {
		r.cfg.ChannelServers = []config.ChannelServerConfig{{
			Name:    "echo",
			Command: []string{echoChannelBin},
			Env: map[string]string{
				"ECHO_CHANNEL_IN":   in,
				"ECHO_CHANNEL_OUT":  out,
				"ECHO_CHANNEL_USER": user,
				"ECHO_CHANNEL_CHAT": "room-1",
			},
			Owner: user,
		}}
	})

	f, err := os.OpenFile(in, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, "/new "+agentName+" "+filepath.Join(r.ws, "demo")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	replies := func() []string {
		data, _ := os.ReadFile(out)
		var texts []string
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if _, text, ok := strings.Cut(l, "\t"); ok {
				texts = append(texts, strings.ReplaceAll(text, `\n`, "\n"))
			}
		}
		return texts
	}
	r.waitFor("a session receipt through the channel server", func() bool {
		return slices.ContainsFunc(replies(), func(s string) bool { return strings.Contains(s, "会话 1") })
	})
	for _, s := range replies() {
		if strings.Contains(s, "配对码") {
			t.Errorf("the owner was asked to pair in room-1:\n%s", s)
		}
	}
}

// --- audit export ---------------------------------------------------------

// runFerry runs the built ferry command against the rig's home and returns its
// stdout, stderr and exit code.
func (r *rig) runFerry(args ...string) (string, string, int) {
	r.t.Helper()
	cmd := exec.Command(ferryBin, args...)
	cmd.Env = append(os.Environ(), "FERRY_HOME="+r.home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		r.t.Fatalf("run ferry %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

// B4: ferry audit export writes the audit log as CSV without any message body,
// and refuses a bad --since or --format with exit code 2.
func TestAuditExport(t *testing.T) {
	r := newRig(t)
	r.newSession()
	const body = "echo audit-7f3c" // 15 characters
	mark := r.markOwner()
	r.send(body)
	r.waitOwnerTextFrom(mark, "audit-7f3c")
	// The inbound row is written once the turn has started, which can be just
	// after the echo is on screen.
	audit, ok := r.store.(interface {
		AuditSince(context.Context, time.Time) ([]ports.AuditEntry, error)
	})
	if !ok {
		t.Fatal("the rig's store cannot list the audit log")
	}
	r.waitFor("the inbound audit row", func() bool {
		entries, err := audit.AuditSince(context.Background(), time.Now().Add(-time.Hour))
		return err == nil && slices.ContainsFunc(entries, func(e ports.AuditEntry) bool {
			return e.Kind == "inbound" && e.Detail == "len=15"
		})
	})

	stdout, stderr, code := r.runFerry("audit", "export", "--since", "1h")
	if code != 0 {
		t.Fatalf("ferry audit export exited %d:\n%s", code, stderr)
	}
	rows, err := csv.NewReader(strings.NewReader(stdout)).ReadAll()
	if err != nil {
		t.Fatalf("the export is not CSV: %v\n%s", err, stdout)
	}
	if len(rows) == 0 || strings.Join(rows[0], ",") != "at,kind,endpoint,sender_id,session_id,detail" {
		t.Fatalf("unexpected header:\n%s", stdout)
	}
	if !slices.ContainsFunc(rows[1:], func(row []string) bool { return row[1] == "inbound" && row[5] == "len=15" }) {
		t.Errorf("no inbound row with detail len=15:\n%s", stdout)
	}
	if strings.Contains(stdout, "audit-7f3c") {
		t.Errorf("the export carries a message body:\n%s", stdout)
	}

	for _, args := range [][]string{
		{"audit", "export", "--since", "7x"},
		{"audit", "export", "--format", "json"},
	} {
		if _, stderr, code := r.runFerry(args...); code != 2 {
			t.Errorf("ferry %s exited %d, want 2:\n%s", strings.Join(args, " "), code, stderr)
		}
	}
}

// --- Feishu approval cards ------------------------------------------------

// waitCard waits for a card to the Feishu owner after the first from cards and
// returns it.
func (r *rig) waitCard(from int) feishutest.Sent {
	r.t.Helper()
	var hit feishutest.Sent
	r.waitFor(fmt.Sprintf("a Feishu card after #%d", from), func() bool {
		cards := r.fs.Cards()
		for i := from; i < len(cards); i++ {
			if cards[i].OpenID == feishuOwner {
				hit = cards[i]
				return true
			}
		}
		return false
	})
	return hit
}

// B5: a Feishu approval arrives as a card whose buttons carry its ID, a click
// decides it, and a text verdict still works.
func TestFeishuCardApproval(t *testing.T) {
	r := newRig(t, withFeishu())
	r.feishuSend("/new " + agentName + " " + filepath.Join(r.ws, "demo"))
	r.waitFeishuFrom(0, "会话 1")

	cardMark := len(r.fs.Cards())
	r.feishuSend("ask execute " + filepath.Join(r.ws, "demo", "run.sh"))
	card := r.waitCard(cardMark)
	id := r.approvalIDFrom(card.Text)
	if want := r.tr("approval.card_title", map[string]any{"ID": id}); card.Card.Title != want {
		t.Errorf("card title = %q, want %q", card.Card.Title, want)
	}

	// The fake agent offers allow_always, so all three buttons are there.
	var verdicts []string
	var allow feishu.CardValue
	for _, b := range card.Card.Buttons {
		if b.Value.Approval != id {
			t.Errorf("button %q carries approval %q, want %q", b.Text, b.Value.Approval, id)
		}
		verdicts = append(verdicts, b.Value.Verdict)
		if b.Value.Verdict == "y" {
			allow = b.Value
		}
	}
	if !slices.Equal(verdicts, []string{"y", "a", "n"}) {
		t.Fatalf("button verdicts = %v, want [y a n]", verdicts)
	}

	mark := len(r.feishuTexts())
	ctx, stop := context.WithTimeout(context.Background(), settle)
	defer stop()
	toast, ok := r.fs.Click(ctx, feishuOwner, allow)
	if !ok || toast != r.tr("approval.card_received", nil) {
		t.Fatalf("click answered toast=%q ok=%v, want %q", toast, ok, r.tr("approval.card_received", nil))
	}
	r.waitFeishuFrom(mark, "allowed")

	// A second approval, decided by text rather than by a click.
	cardMark = len(r.fs.Cards())
	r.feishuSend("ask execute " + filepath.Join(r.ws, "demo", "again.sh"))
	id2 := r.approvalIDFrom(r.waitCard(cardMark).Text)
	mark = len(r.feishuTexts())
	r.feishuSend("n " + id2)
	r.waitFeishuFrom(mark, "denied")
}
