package command

import (
	"slices"
	"testing"

	"github.com/TomwaltW/ferry/internal/ports"
)

// TestChineseKeyboard covers what a phone or desktop WeChat with a Chinese
// input method actually types: "、" or "／" for "/", the command word and its
// number without a space, and full-width digits.
func TestChineseKeyboard(t *testing.T) {
	tests := []struct {
		text string
		want ports.Command
	}{
		// "、" and "／" stand for "/" in front of a command word.
		{"、ls", ports.Command{Name: "ls"}},
		{"、new kimi ~/code/demo", ports.Command{Name: "new", Args: []string{"kimi", "~/code/demo"}}},
		{"／stop", ports.Command{Name: "stop"}},
		{"／USE 2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"、help", ports.Command{Name: "help"}},

		// The aliases added for the commands that had none.
		{"切换 2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换 2 force", ports.Command{Name: "use", Args: []string{"2", "force"}}},
		{"文件", ports.Command{Name: "file"}},
		{"文件 3", ports.Command{Name: "file", Args: []string{"3"}}},
		{"配对 123456", ports.Command{Name: "pair", Args: []string{"123456"}}},
		{"语言 en", ports.Command{Name: "lang", Args: []string{"en"}}},

		// A number glued to the command word.
		{"切换2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换12 force", ports.Command{Name: "use", Args: []string{"12", "force"}}},
		{"关闭2", ports.Command{Name: "close", Args: []string{"2"}}},
		{"文件3", ports.Command{Name: "file", Args: []string{"3"}}},
		{"配对123456", ports.Command{Name: "pair", Args: []string{"123456"}}},
		{"/use2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"、use2", ports.Command{Name: "use", Args: []string{"2"}}},

		// Full-width digits, glued or not.
		{"切换２", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换 １２", ports.Command{Name: "use", Args: []string{"12"}}},
		{"/file ３", ports.Command{Name: "file", Args: []string{"3"}}},
		{"配对 １２３４５６", ports.Command{Name: "pair", Args: []string{"123456"}}},

		// A full-width space separates words like any other.
		{"切换　2", ports.Command{Name: "use", Args: []string{"2"}}},
	}
	p := New()
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := p.Parse(tt.text)
			if !ok {
				t.Fatalf("Parse(%q) = not a command, want %+v", tt.text, tt.want)
			}
			if got.Name != tt.want.Name || !slices.Equal(got.Args, tt.want.Args) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

// TestChineseKeyboardLeavesTextAlone pins what the loosening above must not
// turn into a command.
func TestChineseKeyboardLeavesTextAlone(t *testing.T) {
	notCommands := []string{
		"、",
		"／",
		"、好的",  // "、" in front of anything but a command word
		"、会话",  // aliases never take a slash
		"、/ls", // the "//" escape spelt with "、" is still text
		"、、ls", // only one leading character is read as "/"
		"切换到2", // only digits may follow the word
		"文件夹",  // a longer word is not the alias
		"文件3份", // digits must end the word
		"停2",   // /stop takes no number
		"新建2",  // nor does /new
		"/ls2", // nor /ls
		"帮助1",  // nor /help
		"a、ls", // "、" only counts at the very start
	}
	p := New()
	for _, text := range notCommands {
		if got, ok := p.Parse(text); ok {
			t.Errorf("Parse(%q) = %+v, want not a command", text, got)
		}
	}
	// As with "停 一下", a whole first word that is an alias makes a command;
	// the core answers a bad argument with the usage hint.
	if got, ok := p.Parse("切换 到 2 号会话，谢谢"); !ok || got.Name != "use" {
		t.Errorf("Parse = (%+v, %v), want use with stray arguments", got, ok)
	}

	// Digits that are not a whole word keep their full width: only a number
	// argument is rewritten.
	got, ok := p.Parse("/new kimi ~/代码/项目２")
	if !ok || !slices.Equal(got.Args, []string{"kimi", "~/代码/项目２"}) {
		t.Errorf("Parse(/new with a full-width digit in the path) = (%+v, %v), want the path untouched", got, ok)
	}
	got, ok = p.Parse("/mode level １")
	if !ok || !slices.Equal(got.Args, []string{"level", "１"}) {
		t.Errorf("Parse(/mode level １) = (%+v, %v), want /mode arguments untouched", got, ok)
	}
}
