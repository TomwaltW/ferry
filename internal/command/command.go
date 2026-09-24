package command

import (
	"strings"
	"unicode"

	"github.com/TomwaltW/ferry/internal/ports"
)

// heads maps every accepted first word to a command name. A message is a
// command only when its first word is one of these keys exactly, so "停一下"
// is ordinary text even though "停" is a command.
var heads = map[string]string{
	"/ls":     "ls",
	"会话":      "ls",
	"/new":    "new",
	"新建":      "new",
	"/use":    "use",
	"/agents": "agents",
	"/stop":   "stop",
	"停":       "stop",
	"停止":      "stop",
	"/close":  "close",
	"/mode":   "mode",
	"/pull":   "pull",
	"收件":      "pull",
	"/file":   "file",
	"/lang":   "lang",
	"/help":   "help",
	"帮助":      "help",
	"/pair":   "pair",
}

// strict are Chinese words that stand for a command only when the rest of the
// message fits that command exactly (see fits). They are everyday words, and
// Chinese writers set English words off with spaces: "文件 main.go 里有个 bug"
// and "关闭 2 个测试" are messages for the agent, not /file and /close 2.
var strict = map[string]string{
	"切换":  "use",
	"切换到": "use",
	"切到":  "use",
	"关闭":  "close",
	"文件":  "file",
	"语言":  "lang",
	"配对":  "pair",
}

// numbered are the commands whose first argument is a number. The number may
// be glued to the command word ("切换2", "/use2"), which is how a Chinese
// keyboard types it, and full-width digits in it are read as ASCII.
var numbered = map[string]bool{"use": true, "close": true, "file": true, "pair": true}

// langWords are the arguments /lang understands (core cmdLang), which is all
// the Chinese 语言 may be followed by.
var langWords = map[string]bool{
	"zh": true, "zh-cn": true, "zh-hans": true, "cn": true, "中文": true,
	"en": true, "en-us": true, "english": true, "英文": true, "英语": true,
}

// closingMarks are the sentence endings a spoken command arrives with: WeChat
// transcribes "停" as "停。". The approval package trims the same set from a
// verdict (approval.closingMarks).
const closingMarks = "。．.！!～~"

// New returns the parser for the chat command set.
func New() ports.Commands { return parser{} }

type parser struct{}

// Parse reports whether the whole message is a command. Arguments of the
// slash forms are not checked here: "/use abc" still parses as use, and the
// core answers with the usage hint.
func (parser) Parse(text string) (ports.Command, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || strings.HasPrefix(fields[0], "//") {
		return ports.Command{}, false
	}
	name, args, strictWord, ok := lookup(fields[0])
	if !ok && len(fields) == 1 {
		name, args, strictWord, ok = lookup(strings.TrimRight(fields[0], closingMarks))
	}
	if !ok {
		return ports.Command{}, false
	}
	args = append(args, fields[1:]...)
	if numbered[name] && len(args) != 0 {
		if n := strings.TrimRight(args[0], closingMarks); allDigits(n) {
			args[0] = foldWidth(n)
		}
	}
	if strictWord && !fits(name, args) {
		return ports.Command{}, false
	}
	if len(args) == 0 {
		return ports.Command{Name: name}, true
	}
	return ports.Command{Name: name, Args: args}, true
}

// lookup names the command a first word stands for, with the number glued to
// it, if any, and whether the word is one of the strict ones. Full-width
// letters, digits and "／" count as their ASCII selves, and a leading "、"
// counts as "/": desktop WeChat with a pinyin input method types it for the
// "/" key. Nothing else about the word is loosened: "、ls" is /ls, "、好的" is
// still text.
func lookup(head string) (string, []string, bool, bool) {
	head = foldWidth(head)
	if rest, ok := strings.CutPrefix(head, "、"); ok {
		head = "/" + rest
	}
	head = strings.ToLower(head)
	if name, ok := heads[head]; ok {
		return name, nil, false, true
	}
	if name, ok := strict[head]; ok {
		return name, nil, true, true
	}
	word := strings.TrimRightFunc(head, isDigit)
	if word == head {
		return "", nil, false, false
	}
	glued := []string{head[len(word):]}
	if name, ok := heads[word]; ok && numbered[name] {
		return name, glued, false, true
	}
	if name, ok := strict[word]; ok && numbered[name] {
		return name, glued, true, true
	}
	return "", nil, false, false
}

// fits reports whether the arguments after a strict word are exactly what its
// command takes: 切换 <n> [force], 关闭 [<n>], 文件 [<n>], 配对 <code> and
// 语言 [<language>].
func fits(name string, args []string) bool {
	switch name {
	case "use":
		return (len(args) == 1 || len(args) == 2 && strings.EqualFold(args[1], "force")) && allDigits(args[0])
	case "close", "file":
		return len(args) == 0 || len(args) == 1 && allDigits(args[0])
	case "pair":
		return len(args) == 1 && allDigits(args[0])
	case "lang":
		return len(args) == 0 || len(args) == 1 && langWords[strings.ToLower(args[0])]
	}
	return false
}

// isDigit reports an ASCII or a full-width decimal digit.
func isDigit(r rune) bool {
	return ('0' <= r && r <= '9') || ('０' <= r && r <= '９')
}

// allDigits reports a non-empty word made only of digits.
func allDigits(word string) bool {
	return word != "" && strings.IndexFunc(word, func(r rune) bool { return !isDigit(r) }) < 0
}

// foldWidth reads full-width ASCII (U+FF01 to U+FF5E: "／", "ｌｓ", "２") as
// plain ASCII, which is what a Chinese keyboard in full-width mode types.
func foldWidth(word string) string {
	return strings.Map(func(r rune) rune {
		if '！' <= r && r <= '～' {
			return r - ('！' - '!')
		}
		return r
	}, word)
}

// Unescape strips one leading slash from a message that starts with "//" so
// that the rest can be sent to the agent as ordinary text.
func (parser) Unescape(text string) (string, bool) {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "//") {
		return text, false
	}
	return trimmed[1:], true
}

var _ ports.Commands = parser{}
