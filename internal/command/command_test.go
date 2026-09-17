package command

import (
	"slices"
	"testing"

	"github.com/TomwaltW/ferry/internal/ports"
)

func TestParseTable(t *testing.T) {
	tests := []struct {
		text string
		want ports.Command
		ok   bool
	}{
		// Every command of spec 3.5, slash form.
		{"/ls", ports.Command{Name: "ls"}, true},
		{"/new kimi", ports.Command{Name: "new", Args: []string{"kimi"}}, true},
		{"/new kimi ~/code/demo", ports.Command{Name: "new", Args: []string{"kimi", "~/code/demo"}}, true},
		{"/use 2", ports.Command{Name: "use", Args: []string{"2"}}, true},
		{"/agents", ports.Command{Name: "agents"}, true},
		{"/stop", ports.Command{Name: "stop"}, true},
		{"/close", ports.Command{Name: "close"}, true},
		{"/close 3", ports.Command{Name: "close", Args: []string{"3"}}, true},
		{"/mode approval auto", ports.Command{Name: "mode", Args: []string{"approval", "auto"}}, true},
		{"/pull", ports.Command{Name: "pull"}, true},
		{"/file 1", ports.Command{Name: "file", Args: []string{"1"}}, true},
		{"/lang en", ports.Command{Name: "lang", Args: []string{"en"}}, true},
		{"/help", ports.Command{Name: "help"}, true},
		{"/pair 123456", ports.Command{Name: "pair", Args: []string{"123456"}}, true},

		// Chinese aliases.
		{"会话", ports.Command{Name: "ls"}, true},
		{"新建 kimi", ports.Command{Name: "new", Args: []string{"kimi"}}, true},
		{"新建 kimi ~/code", ports.Command{Name: "new", Args: []string{"kimi", "~/code"}}, true},
		{"停", ports.Command{Name: "stop"}, true},
		{"关闭", ports.Command{Name: "close"}, true},
		{"关闭 2", ports.Command{Name: "close", Args: []string{"2"}}, true},
		{"收件", ports.Command{Name: "pull"}, true},
		{"帮助", ports.Command{Name: "help"}, true},

		// The name is case insensitive.
		{"/LS", ports.Command{Name: "ls"}, true},
		{"/Use 2", ports.Command{Name: "use", Args: []string{"2"}}, true},
		{"/HELP", ports.Command{Name: "help"}, true},

		// Surrounding and repeated whitespace is not significant.
		{"  /ls  ", ports.Command{Name: "ls"}, true},
		{"/new   kimi    ~/code", ports.Command{Name: "new", Args: []string{"kimi", "~/code"}}, true},
		{"\n/stop\n", ports.Command{Name: "stop"}, true},
		{" 会话 ", ports.Command{Name: "ls"}, true},

		// Wrong arguments still parse; the core replies with the usage hint.
		{"/use abc", ports.Command{Name: "use", Args: []string{"abc"}}, true},
		{"/use", ports.Command{Name: "use"}, true},
		{"/new", ports.Command{Name: "new"}, true},
		{"/pair abc", ports.Command{Name: "pair", Args: []string{"abc"}}, true},
		{"/lang", ports.Command{Name: "lang"}, true},
		{"/ls extra", ports.Command{Name: "ls", Args: []string{"extra"}}, true},

		// Not commands.
		{"", ports.Command{}, false},
		{"   ", ports.Command{}, false},
		{"hello", ports.Command{}, false},
		{"/", ports.Command{}, false},
		{"/nope", ports.Command{}, false},
		{"/ls/", ports.Command{}, false},
		{"//ls", ports.Command{}, false},
		{"// /ls", ports.Command{}, false},
		{"给我 /ls", ports.Command{}, false},
		{"please /help me", ports.Command{}, false},
	}
	p := New()
	for _, tt := range tests {
		got, ok := p.Parse(tt.text)
		if ok != tt.ok {
			t.Errorf("Parse(%q) ok = %v, want %v", tt.text, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Name != tt.want.Name || !slices.Equal(got.Args, tt.want.Args) {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.text, got, tt.want)
		}
	}
}

func TestUnescape(t *testing.T) {
	tests := []struct {
		text string
		want string
		ok   bool
	}{
		{"//ls", "/ls", true},
		{"//help me", "/help me", true},
		{"//not a command", "/not a command", true},
		{"///ls", "//ls", true},
		{"  //ls", "/ls", true},
		{"/ls", "/ls", false},
		{"hello", "hello", false},
		{"", "", false},
		{"a // b", "a // b", false},
	}
	p := New()
	for _, tt := range tests {
		got, ok := p.Unescape(tt.text)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Unescape(%q) = (%q, %v), want (%q, %v)", tt.text, got, ok, tt.want, tt.ok)
		}
	}

	// What Unescape returns must not parse as a command again: the core sends
	// it to the agent as text.
	if _, ok := p.Parse("//ls"); ok {
		t.Error(`Parse("//ls") = command, want not a command`)
	}
}

func TestAliasWholeMessageOnly(t *testing.T) {
	// A Chinese alias is a command only when it is the whole first word.
	notCommands := []string{
		"停一下",
		"先停",
		"停止会话",
		"会话列表",
		"我的会话",
		"新建一个会话",
		"关闭它",
		"帮助我一下",
		"收件箱",
	}
	p := New()
	for _, text := range notCommands {
		if got, ok := p.Parse(text); ok {
			t.Errorf("Parse(%q) = %+v, want not a command", text, got)
		}
	}

	// The line is drawn at the word, not at the message: "停 一下" keeps "停" as
	// its own first word and is the stop command with a stray argument, while
	// "停一下" is one word and therefore ordinary text.
	if got, ok := p.Parse("停 一下"); !ok || got.Name != "stop" {
		t.Errorf(`Parse("停 一下") = (%+v, %v), want the stop command with an argument`, got, ok)
	}
	if got, ok := p.Parse("停一下"); ok {
		t.Errorf(`Parse("停一下") = %+v, want not a command`, got)
	}
}
