// host plugin: observe remote host state via SSH.
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// SSHs into a remote host and runs gather.sh, parsing its NDJSON output
// into structured records (OS info, packages, services, mounts, network,
// users) for pudl to import.
//
// Capabilities: discover, observe. (The SDK also advertises "plan" because
// it requires a Plan func; this plugin's Plan returns no actions.)
//
// Config options:
//
//	host  - hostname or IP address (required)
//	user  - SSH user (default: "root")
//	key   - path to SSH private key (optional, uses ssh-agent if absent)
//	port  - SSH port (default: 22)
//
// Sealed inputs (via secrets):
//
//	SSH_PASS - SSH password (optional, used with sshpass if present)
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/chau/mu/sdk/muplugin"
)

//go:embed gather.sh
var gatherScript string

func main() {
	(&muplugin.Plugin{
		Name:        "host",
		Version:     "0.1.0",
		Description: "Observe remote host state via SSH",
		Produces:    []string{"host_state"},
		ConfigSchema: map[string]any{
			"host": map[string]any{
				"type":        "string",
				"description": "Hostname or IP address of the remote machine",
			},
			"user": map[string]any{
				"type":        "string",
				"default":     "root",
				"description": "SSH login user",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Path to SSH private key",
			},
			"port": map[string]any{
				"type":        "integer",
				"default":     22,
				"description": "SSH port",
			},
		},
		Plan:    plan,
		Observe: observe,
	}).Main()
}

// plan is required by the SDK but this plugin produces no build actions —
// it is purely an observer. Returns an empty action list.
func plan(_ context.Context, _ muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{},
		Outputs: map[string]string{},
	}, nil
}

// observe SSHs into the configured host, executes the embedded gather.sh
// over stdin, and parses each non-blank stdout line as a JSON record.
func observe(ctx context.Context, req muplugin.ObserveRequest) (muplugin.ObserveResponse, error) {
	cfg := req.Target.Config
	host, _ := cfg["host"].(string)
	if host == "" {
		return muplugin.ObserveResponse{Error: "observe failed: host plugin requires \"host\" in config"}, nil
	}

	sshCmd, err := sshBaseCmd(cfg, req.Secrets)
	if err != nil {
		return muplugin.ObserveResponse{Error: "observe failed: " + err.Error()}, nil
	}
	sshCmd = append(sshCmd, "bash", "-s")

	cmd := exec.CommandContext(ctx, sshCmd[0], sshCmd[1:]...)
	cmd.Stdin = strings.NewReader(gatherScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		return muplugin.ObserveResponse{
			Error: fmt.Sprintf("remote gather failed (exit %d): %s",
				exitCode, strings.TrimSpace(stderr.String())),
		}, nil
	}

	records := []any{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return muplugin.ObserveResponse{
				Error: fmt.Sprintf("observe failed: parse record: %v", err),
			}, nil
		}
		records = append(records, rec)
	}

	return muplugin.ObserveResponse{
		Current: map[string]any{"records": records},
	}, nil
}

// sshBaseCmd builds the SSH command vector from config and secrets, mirroring
// the bb plugin's logic exactly. When SSH_PASS is present, wraps with sshpass
// and verifies the helper is installed.
func sshBaseCmd(cfg map[string]any, secrets map[string]string) ([]string, error) {
	host, _ := cfg["host"].(string)
	user := "root"
	if u, ok := cfg["user"].(string); ok && u != "" {
		user = u
	}

	port := "22"
	if p, ok := cfg["port"]; ok {
		switch v := p.(type) {
		case string:
			if v != "" {
				port = v
			}
		case float64:
			port = strconv.Itoa(int(v))
		case int:
			port = strconv.Itoa(v)
		}
	}

	key, _ := cfg["key"].(string)
	pass := secrets["SSH_PASS"]

	opts := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-p", port,
	}
	if key != "" {
		opts = append(opts, "-i", key)
	}
	dest := user + "@" + host

	if pass != "" {
		if err := checkSSHPass(); err != nil {
			return nil, err
		}
		cmd := []string{"sshpass", "-p", pass, "ssh"}
		cmd = append(cmd, opts...)
		cmd = append(cmd, dest)
		return cmd, nil
	}
	cmd := []string{"ssh"}
	cmd = append(cmd, opts...)
	cmd = append(cmd, "-o", "BatchMode=yes", dest)
	return cmd, nil
}

// checkSSHPass verifies sshpass is installed when password auth is needed.
func checkSSHPass() error {
	if _, err := exec.LookPath("sshpass"); err != nil {
		return fmt.Errorf("sshpass is required for password auth but not found. Install it: brew install hudochenkov/sshpass/sshpass (macOS) or apt install sshpass (Linux)")
	}
	return nil
}
