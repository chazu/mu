package coordinator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

func captureMissingSchemaWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := missingSchemaOut
	missingSchemaOut = &buf
	t.Cleanup(func() { missingSchemaOut = prev })
	return &buf
}

// A plan-capable plugin without a config_schema gets one warning per
// process, however many configured targets use it.
func TestPlanWarnsOnceForSchemaLessPlugin(t *testing.T) {
	buf := captureMissingSchemaWarnings(t)
	t.Setenv("HOME", t.TempDir())
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//a", Toolchain: "noschema-warn", Config: map[string]any{"x": 1}},
				{Name: "//b", Toolchain: "noschema-warn", Config: map[string]any{"y": "z"}},
			},
			Plugins: []config.PluginDef{
				{Name: "noschema-warn", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}
	if _, err := c.Plan(context.Background(), []string{"//a", "//b"}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := c.Plan(context.Background(), []string{"//a"}); err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	out := buf.String()
	if n := strings.Count(out, "warning:"); n != 1 {
		t.Fatalf("got %d warnings, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `plugin "noschema-warn" declares no config_schema`) {
		t.Fatalf("warning = %q", out)
	}
}

func TestMissingSchemaWarningConditions(t *testing.T) {
	buf := captureMissingSchemaWarnings(t)
	cfg := map[string]any{"k": "v"}
	cases := []struct {
		name   string
		target config.Target
		info   *plugin.DiscoverResponse
	}{
		{"no config", config.Target{Name: "//t", Toolchain: "w-noconfig"}, &plugin.DiscoverResponse{}},
		{"has schema", config.Target{Name: "//t", Toolchain: "w-schema", Config: cfg},
			&plugin.DiscoverResponse{ConfigSchema: map[string]any{"k": map[string]any{"type": "string"}}}},
		{"not plan-capable", config.Target{Name: "//t", Toolchain: "w-provider", Config: cfg},
			&plugin.DiscoverResponse{Capabilities: []string{"discover", "resolve_secret"}}},
		{"no discover info", config.Target{Name: "//t", Toolchain: "w-nil", Config: cfg}, nil},
	}
	for _, tc := range cases {
		warnMissingConfigSchema(tc.target, tc.info)
		if buf.Len() != 0 {
			t.Fatalf("%s: unexpected warning %q", tc.name, buf.String())
		}
	}
	warnMissingConfigSchema(config.Target{Name: "//t", Toolchain: "w-plan", Config: cfg},
		&plugin.DiscoverResponse{Capabilities: []string{"discover", "plan"}})
	if !strings.Contains(buf.String(), `plugin "w-plan"`) {
		t.Fatalf("plan-capable plugin without schema: got %q", buf.String())
	}
}
