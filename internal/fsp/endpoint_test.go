package fsp

import "testing"

func TestEndpointRoundTrip(t *testing.T) {
	cases := map[string]EndpointID{
		"plain":            {Platform: PlatformWeChat, BotID: "bot1", PeerID: "peer1"},
		"colon in peer":    {Platform: PlatformWeChat, BotID: "bot1", PeerID: "wxid:a:b"},
		"empty peer":       {Platform: PlatformFake, BotID: "bot1", PeerID: ""},
		"unknown platform": {Platform: "slack", BotID: "b", PeerID: "p"},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseEndpoint(want.String())
			if err != nil {
				t.Fatalf("ParseEndpoint(%q) failed: %v", want.String(), err)
			}
			if got != want {
				t.Errorf("round trip gave %+v, want %+v", got, want)
			}
		})
	}
}

func TestEndpointStringFormat(t *testing.T) {
	e := EndpointID{Platform: PlatformWeChat, BotID: "bot1", PeerID: "peer1"}
	if got, want := e.String(), "wechat:bot1:peer1"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

func TestParseEndpointRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "wechat", "wechat:bot"} {
		if got, err := ParseEndpoint(s); err == nil {
			t.Errorf("ParseEndpoint(%q) = %+v, want an error", s, got)
		}
	}
}
