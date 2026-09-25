package core

import (
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// voice is a WeChat voice message as the adapter hands it over: the words
// WeChat heard as the text, the recording as an attachment.
func (h *harness) voice(heard string) {
	h.t.Helper()
	h.send(ownerID, heard, voiceNote)
}

func TestVoiceReceiptSaysWhatWasHeard(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.voice("帮我看看 retry 的逻辑")
	receipt := h.waitText("turn.accepted_voice|")
	for _, want := range []string{"Heard=帮我看看 retry 的逻辑", "Slot=1", "Agent=" + testAgent, "StopHint=turn.voice_stop_hint|"} {
		if !strings.Contains(receipt.Text, want) {
			t.Errorf("receipt = %q, want it to carry %q", receipt.Text, want)
		}
	}
	if n := h.countText("turn.accepted|"); n != 0 {
		t.Errorf("%d plain receipts as well, want the voice one alone", n)
	}

	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	// The recording itself stays out: no agent can play it.
	want := "prompt.voice|zh-CN\n帮我看看 retry 的逻辑"
	if got := agent.prompted()[0]; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// A recording that failed to download costs no reply of its own: its words
// arrived as the text, which is all the agent gets anyway.
func TestVoiceDownloadFailureIsQuiet(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	before := len(h.outbox.all())

	h.send(ownerID, "看一下日志", fsp.Attachment{Kind: "voice", Name: "voice.silk"})
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	h.waitText("turn.accepted_voice|")
	h.settle()
	sent := h.outbox.all()[before:]
	if len(sent) != 2 { // the receipt, then the help text settle asked for
		t.Errorf("replies = %q, want the receipt alone", h.outbox.texts()[before:])
	}
	if n := h.countText("media.download_failed"); n != 0 {
		t.Errorf("%d download failures reported for a voice recording, want none", n)
	}
}

func TestVoiceWithoutTranscription(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.voice("")
	h.waitText("media.voice_no_text|")
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Errorf("the agent was prompted %d times with a recording it cannot hear, want 0", n)
	}
	if n := h.countText("turn.accepted"); n != 0 {
		t.Errorf("%d receipts, want none", n)
	}

	// A picture without words is still for the agent.
	h.send(ownerID, "", fsp.Attachment{Kind: "image", Name: "a.png", Path: "/tmp/a.png"})
	h.waitFor("the picture's prompt", func() bool { return len(agent.prompted()) == 1 })
}

func TestVoiceReceiptAfterGathering(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("看下这个")
	h.piece("outbox 重试", voiceNote)
	h.clk.Advance(testGather)
	receipt := h.waitText("turn.accepted_voice|")
	if !strings.Contains(receipt.Text, "Heard=outbox 重试 ") {
		t.Errorf("receipt = %q, want only the spoken piece echoed", receipt.Text)
	}
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; !strings.HasPrefix(got, "看下这个\nprompt.voice|zh-CN\noutbox 重试") {
		t.Errorf("prompt = %q, want the typed piece, then the marked transcription", got)
	}
}

func TestVoiceReceiptLongTranscriptionIsCut(t *testing.T) {
	h := newHarness(t)
	h.newSession()

	h.voice(strings.Repeat("长", 3*heardBudget))
	receipt := h.waitText("turn.accepted_voice|")
	if n := strings.Count(receipt.Text, "长"); n > heardBudget {
		t.Errorf("the receipt echoes %d characters, want at most %d", n, heardBudget)
	}
}

// An external session gets no "send 停 if misheard" hint: /stop does not mean
// "cancel this turn" to every kind of external process.
func TestVoiceReceiptOfExternalSession(t *testing.T) {
	h := newExternalHarness(t)
	h.attach(control.AttachRequest{Kind: control.KindProxy, Agent: testAgent, Cwd: h.root, AllowInject: true})
	h.waitFor("the external session to be announced", func() bool { return len(h.outbox.all()) != 0 })

	select {
	case h.im.channel() <- fsp.Inbound{
		Endpoint: h.externalEP(), SenderID: ownerID, MessageID: "voice-1",
		Text: "继续", Attachments: []fsp.Attachment{voiceNote}, ReplyCtx: "ctx", ReceivedAt: h.clk.Now(),
	}:
	case <-time.After(3 * time.Second):
		t.Fatal("inbound channel blocked")
	}
	receipt := h.waitText("turn.accepted_voice|")
	// The fake catalogue sorts the arguments, so StopHint comes last.
	if !strings.HasSuffix(receipt.Text, "StopHint=") {
		t.Errorf("receipt = %q, want an empty StopHint for an external session", receipt.Text)
	}
}
