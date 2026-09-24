package command

import (
	"slices"
	"testing"

	"github.com/TomwaltW/ferry/internal/ports"
)

// TestChineseKeyboard covers what a phone or desktop WeChat with a Chinese
// input method, or WeChat's voice input, actually sends: "、" or full-width
// characters for ASCII ones, the command word and its number without a space,
// and a trailing full stop.
func TestChineseKeyboard(t *testing.T) {
	tests := []struct {
		text string
		want ports.Command
	}{
		// "、" and full-width characters stand for their ASCII selves.
		{"、ls", ports.Command{Name: "ls"}},
		{"、new kimi ~/code/demo", ports.Command{Name: "new", Args: []string{"kimi", "~/code/demo"}}},
		{"／stop", ports.Command{Name: "stop"}},
		{"／USE 2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"／ｌｓ", ports.Command{Name: "ls"}},
		{"／ｕｓｅ２", ports.Command{Name: "use", Args: []string{"2"}}},
		{"、help", ports.Command{Name: "help"}},

		// The aliases added for the commands that had none, and 停止.
		{"切换 2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换 2 force", ports.Command{Name: "use", Args: []string{"2", "force"}}},
		{"切换到 2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切到3", ports.Command{Name: "use", Args: []string{"3"}}},
		{"文件", ports.Command{Name: "file"}},
		{"文件 3", ports.Command{Name: "file", Args: []string{"3"}}},
		{"配对 123456", ports.Command{Name: "pair", Args: []string{"123456"}}},
		{"语言 en", ports.Command{Name: "lang", Args: []string{"en"}}},
		{"语言 英文", ports.Command{Name: "lang", Args: []string{"英文"}}},
		{"语言", ports.Command{Name: "lang"}},
		{"停止", ports.Command{Name: "stop"}},

		// A number glued to the command word.
		{"切换2", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换12 force", ports.Command{Name: "use", Args: []string{"12", "force"}}},
		{"切换到2", ports.Command{Name: "use", Args: []string{"2"}}},
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

		// The full stop voice input ends a sentence with.
		{"停。", ports.Command{Name: "stop"}},
		{"停！", ports.Command{Name: "stop"}},
		{"停止。", ports.Command{Name: "stop"}},
		{"新建。", ports.Command{Name: "new"}},
		{"会话。", ports.Command{Name: "ls"}},
		{"切换2。", ports.Command{Name: "use", Args: []string{"2"}}},
		{"切换 2。", ports.Command{Name: "use", Args: []string{"2"}}},
		{"关闭 2。", ports.Command{Name: "close", Args: []string{"2"}}},
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
		"。",
		"、好的",  // "、" in front of anything but a command word
		"、会话",  // aliases never take a slash
		"、/ls", // the "//" escape spelt with "、" is still text
		"、、ls", // only one leading character is read as "/"
		"文件夹",  // a longer word is not the alias
		"文件3份", // digits must end the word
		"停2",   // /stop takes no number
		"新建2",  // nor does /new
		"/ls2", // nor /ls
		"帮助1",  // nor /help
		"a、ls", // "、" only counts at the very start
		"停？",   // a question is not a command

		// A Chinese word that is also an everyday word is a command only when
		// what follows fits the command exactly.
		"文件 main.go 里有个 bug",
		"切换 Python 版本",
		"切换到 新的分支",
		"关闭 debug 日志",
		"关闭 2 个测试",
		"切换 2 个参数",
		"语言 用中文回答",
		"配对 蓝牙耳机",
		"切换",
		"配对",
	}
	p := New()
	for _, text := range notCommands {
		if got, ok := p.Parse(text); ok {
			t.Errorf("Parse(%q) = %+v, want not a command", text, got)
		}
	}

	// The slash forms keep reaching the core with whatever follows, which
	// answers a wrong argument with the usage hint.
	for _, text := range []string{"/use Python", "/close 2 个测试", "/file main.go"} {
		if _, ok := p.Parse(text); !ok {
			t.Errorf("Parse(%q) = not a command, want the slash form parsed", text)
		}
	}

	// Only a number argument is rewritten, and a full stop is only dropped
	// from a message that is a single word or from a number.
	got, ok := p.Parse("/new kimi ~/代码/项目２")
	if !ok || !slices.Equal(got.Args, []string{"kimi", "~/代码/项目２"}) {
		t.Errorf("Parse(/new with a full-width digit in the path) = (%+v, %v), want the path untouched", got, ok)
	}
	got, ok = p.Parse("/mode level １")
	if !ok || !slices.Equal(got.Args, []string{"level", "１"}) {
		t.Errorf("Parse(/mode level １) = (%+v, %v), want /mode arguments untouched", got, ok)
	}
	got, ok = p.Parse("/new kimi .")
	if !ok || !slices.Equal(got.Args, []string{"kimi", "."}) {
		t.Errorf(`Parse("/new kimi .") = (%+v, %v), want the "." directory kept`, got, ok)
	}
}
