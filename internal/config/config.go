package config

// Home returns FERRY_HOME, or ~/.ferry when it is unset.
func Home() string { panic("not implemented") }

// Path returns <home>/config.toml.
func Path(home string) string { panic("not implemented") }

func Default() Config { panic("not implemented") }

// Load returns fsp.ErrNotFound when the file is missing; validation errors
// name the offending field.
func Load(path string) (Config, error) { panic("not implemented") }

// Save writes the file with mode 0600.
func Save(path string, c Config) error { panic("not implemented") }

func (c Config) Validate() error { panic("not implemented") }

// NormalizedRoots expands ~, makes paths absolute and resolves symlinks.
func (c Config) NormalizedRoots() ([]string, error) { panic("not implemented") }
