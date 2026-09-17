package main

import (
	"errors"
	"sync"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
)

// errSilentExit1 and errExit2 let a command pick its exit code without cobra
// printing the error text a second time.
var (
	errSilentExit1 = errors.New("exit 1")
	errExit2       = errors.New("exit 2")
)

func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errExit2):
		return 2
	default:
		return 1
	}
}

// homeDir is FERRY_HOME, or ~/.ferry when it is unset.
func homeDir() string { return config.Home() }

var loadBundle = sync.OnceValue(i18n.Default)

func defaultBundle() *i18n.Bundle { return loadBundle() }

// defaultLang is the language for command line output: whatever the config
// says, or zh-CN before there is a config.
func defaultLang(home string) fsp.Lang {
	cfg, err := config.Load(config.Path(home))
	if err != nil || cfg.Lang == "" {
		return fsp.LangZH
	}
	return cfg.Lang
}
