package wechat

import (
	"context"
	"testing"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

func TestRenderPlain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text is left alone", "just a sentence", "just a sentence"},
		{"heading", "# Title", "【Title】"},
		{"deep heading", "### Deep dive ###", "【Deep dive】"},
		{"bold and italic and code", "**bold** and *italic* and `code`", "bold and italic and code"},
		{"underscore emphasis", "__strong__ and _slanted_", "strong and slanted"},
		{"snake case survives", "call run_the_thing now", "call run_the_thing now"},
		{"link", "see [the docs](https://example.com/a_b) first", "see the docs (https://example.com/a_b) first"},
		{"bare link", "[](https://example.com)", "https://example.com"},
		{"image", "![a cat](https://example.com/cat.png)", "a cat (https://example.com/cat.png)"},
		{
			"code block",
			"Before\n```go\nx := 1\n```\nAfter",
			"Before\n  x := 1\nAfter",
		},
		{
			"table",
			"| name | state |\n| --- | :---: |\n| ferry | ok |",
			"name · state\nferry · ok",
		},
		{
			"a whole answer",
			"## Result\n\nRan **go test** on `./...`:\n\n```\nok  ferry\n```\n\n| package | result |\n|---|---|\n| wechat | ok |\n\nSee [the log](https://ci.example/run/7).",
			"【Result】\n\nRan go test on ./...:\n\n  ok  ferry\n\npackage · result\nwechat · ok\n\nSee the log (https://ci.example/run/7).",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderPlain(tc.in); got != tc.want {
				t.Fatalf("renderPlain(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("render raw keeps markdown", func(t *testing.T) {
		s := loggedIn(t, fakeilink.Options{})
		a, err := New(Options{
			Config: config.WeChatConfig{BaseURL: s.URL, Render: "raw"},
			Creds: ports.Credentials{
				BotToken: s.BotToken(),
				BotID:    s.BotID(),
				OwnerID:  owner,
				BaseURL:  s.URL,
			},
			KV:    newMemKV(),
			Clock: clock.NewFake(epoch),
			Log:   discardLogger(),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		token := s.PushText(peer, "question")
		if _, err := a.Send(context.Background(), fsp.Outbound{
			Endpoint:  a.endpoint(peer),
			Kind:      fsp.OutText,
			Text:      "# Title",
			DedupeKey: "dk-raw",
		}, token); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if sent := s.Sent(); len(sent) != 1 || sent[0].Text != "# Title" {
			t.Fatalf("gateway got %+v, want the markdown untouched", sent)
		}
	})
}
