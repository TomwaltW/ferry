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
	"/close":  "close",
	"关闭":      "close",
	"/mode":   "mode",
	"/pull":   "pull",
	"收件":      "pull",
	"/file":   "file",
	"/lang":   "lang",
	"/help":   "help",
	"帮助":      "help",
	"/pair":   "pair",
}

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
	name, ok := heads[strings.ToLower(head)]
	if !ok {
		return ports.Command{}, false
	}
	cmd := ports.Command{Name: name}
	if len(fields) > 1 {
		cmd.Args = fields[1:]
	}
	return cmd, true
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
