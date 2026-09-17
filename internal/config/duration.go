package config

import (
	"fmt"
	"time"
)

// Duration is written in TOML as a quoted Go duration such as "30s" or "10m".
// go-toml uses the text codec below for both directions.

var (
	_ interface{ MarshalText() ([]byte, error) } = Duration{}
	_ interface{ UnmarshalText([]byte) error }   = (*Duration)(nil)
)

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("config: %q is not a duration such as \"30s\" or \"10m\"", string(text))
	}
	d.Duration = v
	return nil
}
