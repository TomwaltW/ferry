package acl

import (
	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/ports"
)

func New(cfg config.WeChatConfig, st ports.Store, clk clock.Clock) ports.ACL {
	panic("not implemented")
}
