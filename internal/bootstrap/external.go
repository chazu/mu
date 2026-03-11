package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/plugin"
)

// External runs an external bootstrap plugin specified by execPath.
// It spawns the plugin process, validates it via discover, then sends a plan
// request for each toolchain in cfg. The resulting actions are collected but
// not yet executed (the caller is responsible for DAG execution).
func External(ctx context.Context, execPath string, projectRoot string, cfg *config.ProjectConfig, store cas.Store) error {
	proc, err := plugin.StartProcess("bootstrap-external", []string{execPath}, projectRoot)
	if err != nil {
		return fmt.Errorf("start external bootstrap: %w", err)
	}
	defer proc.Close()

	// Validate the plugin via discover.
	disc, err := proc.Discover(ctx)
	if err != nil {
		return fmt.Errorf("external bootstrap discover: %w", err)
	}
	_ = disc // discovery validated protocol version

	// Send a plan request for each toolchain.
	for _, tc := range cfg.Toolchains {
		target := plugin.TargetInfo{
			Name:      "//toolchains:" + tc.Name,
			Toolchain: "bootstrap",
			Config: map[string]any{
				"version":      tc.Config.Version,
				"url":          tc.Config.URL,
				"sha256":       tc.Config.SHA256,
				"strip_prefix": tc.Config.StripPrefix,
			},
		}

		resp, err := proc.Plan(ctx, target, nil, nil)
		if err != nil {
			return fmt.Errorf("external bootstrap plan %s: %w", tc.Name, err)
		}

		// Execute each action from the plan response.
		for _, action := range resp.Actions {
			if err := executeBootstrapAction(ctx, action, projectRoot); err != nil {
				return fmt.Errorf("external bootstrap action %s for %s: %w", action.ID, tc.Name, err)
			}
		}
	}

	return nil
}

// executeBootstrapAction runs a single action from an external bootstrap plugin.
func executeBootstrapAction(ctx context.Context, action plugin.ActionSpec, projectRoot string) error {
	if len(action.Command) == 0 {
		return fmt.Errorf("action %s: empty command", action.ID)
	}

	cmd := exec.CommandContext(ctx, action.Command[0], action.Command[1:]...)
	cmd.Dir = projectRoot
	if len(action.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range action.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("action %s: %w: %s", action.ID, err, out)
	}
	return nil
}
