package feishu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/security"
)

// newClient is the seam the login uses to reach Feishu. Production builds the
// real SDK client; the tests replace it, because logging in must be testable
// without an app and a network.
var newClient = func(opts Options) (Client, error) { return newSDKClient(opts) }

// LoginInteractive asks for the app ID, the app secret and the owner's open
// ID, verifies them with Ping, puts the secret in the keyring and writes the
// rest to the configuration.
func LoginInteractive(ctx context.Context, in io.Reader, out io.Writer, home string) error {
	path := config.Path(home)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	bundle := i18n.Default()
	lang := cfg.Lang
	if lang == "" {
		lang = fsp.LangZH
	}
	// One reader for the whole exchange: a fresh one per question would drop
	// whatever the previous read had buffered.
	reader := bufio.NewReader(in)
	ask := func(key string) (string, error) { return prompt(reader, out, bundle.T(lang, key, nil)) }

	appID, err := ask("cli.feishu_app_id")
	if err != nil {
		return err
	}
	secret, err := ask("cli.feishu_app_secret")
	if err != nil {
		return err
	}
	owner, err := ask("cli.feishu_owner")
	if err != nil {
		return err
	}
	if appID == "" || secret == "" || owner == "" {
		return errors.New("feishu: the app ID, the app secret and the owner open ID are all required")
	}

	feishu := cfg.Feishu.WithDefaults()
	feishu.Enabled = true
	feishu.AppID = appID
	feishu.OwnerOpenID = owner

	client, err := newClient(Options{Config: feishu, Secret: secret, Home: home})
	if err != nil {
		return err
	}
	if err := client.Ping(ctx); err != nil {
		return err
	}

	// The secret lives in the keyring only; the configuration carries the two
	// identifiers, which are not secret.
	if err := security.SaveCredentials(home, fsp.PlatformFeishu, ports.Credentials{
		BotToken: secret,
		BotID:    appID,
		OwnerID:  owner,
	}); err != nil {
		return err
	}
	cfg.Feishu = feishu
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintln(out, bundle.T(lang, "cli.feishu_ok", map[string]any{"App": appID}))
	return nil
}

// prompt writes one question and reads the answer as a whole line.
func prompt(r *bufio.Reader, out io.Writer, question string) (string, error) {
	fmt.Fprintln(out, question)
	line, err := r.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("feishu: read the answer to %q: %w", question, err)
	}
	return strings.TrimSpace(line), nil
}
