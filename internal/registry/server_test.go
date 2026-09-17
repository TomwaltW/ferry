package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// fakeService answers the health route of one of the two services and nothing
// else, which is all the registry ever asks for.
func fakeService(t *testing.T, kind string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	switch kind {
	case "opencode":
		mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"healthy":true,"version":"0.4.2"}`))
		})
	case "qwen":
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
	default:
		t.Fatalf("no fake service for %q", kind)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// serverAgent is a configured agent driven by a local service.
func serverAgent(name, kind, url, secretEnv string) config.AgentConfig {
	return config.AgentConfig{
		Name:   name,
		Server: &config.ServerConfig{Kind: kind, URL: url, SecretEnv: secretEnv},
	}
}

func newResolver(t *testing.T, agents ...config.AgentConfig) *resolver {
	t.Helper()
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return r.(*resolver)
}

func TestResolveServerAgent(t *testing.T) {
	agent := serverAgent("opencode-live", "opencode", "http://127.0.0.1:4096", "")
	r := newResolver(t, agent)

	spec, err := r.Resolve(context.Background(), "opencode-live")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if spec.Server == nil {
		t.Fatal("Resolve returned no server spec for a server agent")
	}
	if spec.Server.Kind != "opencode" || spec.Server.URL != "http://127.0.0.1:4096" {
		t.Errorf("Resolve returned %+v, want the configured service", *spec.Server)
	}
	if len(spec.Command) != 0 {
		t.Errorf("Resolve returned the command %q; a service is not Ferry's to start", spec.Command)
	}
	if spec.Agent != "opencode-live" {
		t.Errorf("Resolve returned agent %q, want opencode-live", spec.Agent)
	}

	if _, err := r.Resolve(context.Background(), "nobody"); !isNotFound(err) {
		t.Errorf("resolving an agent that is not configured gave %v, want fsp.ErrNotFound", err)
	}

	// An agent still has to say exactly one way of being started.
	both := serverAgent("confused", "qwen", "http://127.0.0.1:4170", "")
	both.Command = []string{"qwen"}
	if _, err := New(config.RegistryConfig{}, []config.AgentConfig{both}, t.TempDir(), testLogger()); err == nil {
		t.Error("an agent with both a command and a server was accepted")
	}

	// And at least one.
	if _, err := New(config.RegistryConfig{}, []config.AgentConfig{{Name: "empty"}}, t.TempDir(), testLogger()); err == nil {
		t.Error("an agent with no command, registry or server was accepted")
	}
}

func TestServerSecretFromEnv(t *testing.T) {
	r := newResolver(t,
		serverAgent("opencode-live", "opencode", "http://127.0.0.1:4096", "OPENCODE_SERVER_PASSWORD"),
		serverAgent("qwen-live", "qwen", "http://127.0.0.1:4170", ""),
	)
	ctx := context.Background()

	t.Setenv("OPENCODE_SERVER_PASSWORD", "hunter2")
	spec, err := r.Resolve(ctx, "opencode-live")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if spec.Server.Secret != "hunter2" {
		t.Errorf("the secret came out as %q, want the value of the environment variable", spec.Server.Secret)
	}

	// The variable is read at resolve time, so an agent whose service is not
	// password protected yet still resolves, with no secret.
	t.Setenv("OPENCODE_SERVER_PASSWORD", "")
	spec, err = r.Resolve(ctx, "opencode-live")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if spec.Server.Secret != "" {
		t.Errorf("the secret came out as %q, want it empty", spec.Server.Secret)
	}

	// An agent that names no variable never reads one.
	spec, err = r.Resolve(ctx, "qwen-live")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if spec.Server.Secret != "" {
		t.Errorf("an agent with no secret_env came out with %q", spec.Server.Secret)
	}
}

func TestServerAvailability(t *testing.T) {
	opencode := fakeService(t, "opencode")
	qwen := fakeService(t, "qwen")

	// A service that is not running: a loopback port nothing listens on.
	dead := httptest.NewServer(http.NewServeMux())
	deadURL := dead.URL
	dead.Close()

	r := newResolver(t,
		serverAgent("opencode-live", "opencode", opencode.URL, ""),
		serverAgent("qwen-live", "qwen", qwen.URL, ""),
		serverAgent("stopped", "opencode", deadURL, ""),
	)

	infos := r.Agents(context.Background())
	if len(infos) != 3 {
		t.Fatalf("Agents reported %d agents, want 3", len(infos))
	}

	byName := map[string]bool{}
	for _, info := range infos {
		byName[info.Name] = info.Available
		if info.Source != sourceServer {
			t.Errorf("%s came out as source %q, want %q", info.Name, info.Source, sourceServer)
		}
	}

	for _, name := range []string{"opencode-live", "qwen-live"} {
		if !byName[name] {
			t.Errorf("%s is reported unavailable although its service answered", name)
		}
	}
	if byName["stopped"] {
		t.Error("an agent whose service is not running is reported available")
	}

	for _, info := range infos {
		switch {
		case info.Available && info.Reason != "":
			t.Errorf("%s is available but carries the reason %q", info.Name, info.Reason)
		case !info.Available && info.Reason == "":
			t.Errorf("%s is unavailable with no reason given", info.Name)
		}
	}
}

func TestServerAvailabilityRefusesRemoteURL(t *testing.T) {
	r := newResolver(t, serverAgent("remote", "opencode", "http://192.168.1.10:4096", ""))

	infos := r.Agents(context.Background())
	if len(infos) != 1 || infos[0].Available {
		t.Fatalf("a non-loopback service was reported as %+v, want it unavailable", infos)
	}
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), fsp.ErrNotFound.Error())
}
