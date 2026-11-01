package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

// Digest- and url-installed plugins carry no Script/Command in mu.cue; list
// --discover must materialize them through the resolver like mu build does.
func TestPluginListDiscoverResolvesDigestAndURLPlugins(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "plugin.sh")
	body := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"discover"'*) echo '{"name":"'"$PLUGIN_NAME"'","version":"2.0.0","protocol_version":1,"consumes":["in"],"produces":["out"],"capabilities":["discover"]}' ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	full := "sha256:" + strings.Repeat("d", 64)
	cfg := &config.ProjectConfig{Plugins: []config.PluginDef{
		{Name: "fromdigest", Digest: full},
		{Name: "fromurl", URL: "https://example.test/p.tar.gz", SHA256: strings.Repeat("e", 64)},
	}}
	var resolved []string
	resolve := func(_ context.Context, _ string, p config.PluginDef) (plugin.PluginDef, cas.Digest, error) {
		resolved = append(resolved, p.Name)
		return plugin.PluginDef{Name: p.Name, Command: []string{"env", "PLUGIN_NAME=" + p.Name, "sh", script}}, cas.Digest{}, nil
	}

	out := captureBuildStdout(t, func() int {
		return pluginListDiscoverWith(context.Background(), cfg, root, true, resolve)
	})
	if strings.Join(resolved, ",") != "fromdigest,fromurl" {
		t.Fatalf("resolver called for %v", resolved)
	}
	var items []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(items) != 2 || items[0].Name != "fromdigest" || items[1].Name != "fromurl" || items[0].Version != "2.0.0" {
		t.Fatalf("discover output = %+v", items)
	}
}

func TestPluginListDiscoverReportsResolveFailure(t *testing.T) {
	cfg := &config.ProjectConfig{Plugins: []config.PluginDef{{Name: "missing", Digest: "sha256:" + strings.Repeat("f", 64)}}}
	resolve := func(context.Context, string, config.PluginDef) (plugin.PluginDef, cas.Digest, error) {
		return plugin.PluginDef{}, cas.Digest{}, errors.New("plugin digest not found in CAS")
	}
	code := captureBuildCode(t, func() int {
		return pluginListDiscoverWith(context.Background(), cfg, t.TempDir(), true, resolve)
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
