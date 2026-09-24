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
	"切换":      "use",
	"/agents": "agents",
	"/stop":   "stop",
	"停":       "stop",
	"/close":  "close",
	"关闭":      "close",
	"/mode":   "mode",
	"/pull":   "pull",
	"收件":      "pull",
	"/file":   "file",
	"文件":      "file",
	"/lang":   "lang",
	"语言":      "lang",
	"/help":   "help",
	"帮助":      "help",
	"/pair":   "pair",
	"配对":      "pair",
}

// numbered are the commands whose first argument is a number. The number may
// be glued to the command word ("切换2", "/use2"), which is how a Chinese
// keyboard types it, and full-width digits in it are read as ASCII.
var numbered = map[string]bool{"use": true, "close": true, "file": true, "pair": true}

// slashes are what a Chinese keyboard types for "/": desktop WeChat with a
// pinyin input method turns the key into "、", and full-width mode gives "／".
var slashes = []string{"、", "／"}

// New returns the parser for the chat command set.
func New() ports.Commands { return parser{} }

type parser struct{}

// Parse reports whether the whole message is a command. Arguments are not
// checked here: "/use abc" still parses as use, and the core answers with the
// usage hint.
func (parser) Parse(text string) (ports.Command, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return ports.Command{}, false
	}
	head := fields[0]
	if strings.HasPrefix(head, "//") {
		return ports.Command{}, false
	}
	name, args, ok := lookup(head)
	if !ok {
		return ports.Command{}, false
	}
	args = append(args, fields[1:]...)
	if len(args) == 0 {
		return ports.Command{Name: name}, true
	}
	if numbered[name] {
		args[0] = asciiDigits(args[0])
	}
	return ports.Command{Name: name, Args: args}, true
}

// lookup names the command a first word stands for, with the number that was
// glued to it, if any. A leading "、" or "／" counts as "/", and nothing else
// about the word is loosened: "、ls" is /ls, "、好的" is still text.
func lookup(head string) (string, []string, bool) {
	for _, slash := range slashes {
		if rest, ok := strings.CutPrefix(head, slash); ok {
			head = "/" + rest
			break
		}
	}
	head = strings.ToLower(head)
	if name, ok := heads[head]; ok {
		return name, nil, true
	}
	word := strings.TrimRightFunc(head, isDigit)
	if word == head {
		return "", nil, false
	}
	name, ok := heads[word]
	if !ok || !numbered[name] {
		return "", nil, false
	}
	return name, []string{head[len(word):]}, true
}

// isDigit reports an ASCII or a full-width decimal digit.
func isDigit(r rune) bool {
	return ('0' <= r && r <= '9') || ('０' <= r && r <= '９')
}

// asciiDigits rewrites a word made only of digits with ASCII digits, so that
// "２" reaches the core as "2". Any other word is returned as it is.
func asciiDigits(word string) string {
	if word == "" || strings.IndexFunc(word, func(r rune) bool { return !isDigit(r) }) >= 0 {
		return word
	}
	return strings.Map(func(r rune) rune {
		if '０' <= r && r <= '９' {
			return '0' + (r - '０')
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
