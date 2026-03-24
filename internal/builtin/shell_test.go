package builtin

import (
	"testing"

	"github.com/chau/mu/internal/config"
)

func TestShellPlan_BasicCommand(t *testing.T) {
	target := config.Target{
		Name:      "//deploy",
		Toolchain: "shell",
		Config: map[string]any{
			"command": []any{"echo", "hello"},
		},
	}

	actions, _, err := ShellPlan(target)
	if err != nil {
		t.Fatalf("ShellPlan: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	a := actions[0]
	if a.ID != "run" {
		t.Errorf("ID = %q, want \"run\"", a.ID)
	}
	if len(a.Command) != 2 || a.Command[0] != "echo" || a.Command[1] != "hello" {
		t.Errorf("Command = %v, want [echo hello]", a.Command)
	}
	if !a.Impure {
		t.Error("Impure = false, want true (default)")
	}
	if a.Network {
		t.Error("Network = true, want false (default)")
	}
}

func TestShellPlan_WithEnvAndNetwork(t *testing.T) {
	target := config.Target{
		Name:      "//deploy/prod",
		Toolchain: "shell",
		Config: map[string]any{
			"command": []any{"kubectl", "apply", "-f", "manifest.yaml"},
			"env":     map[string]any{"KUBECONFIG": "/home/user/.kube/config"},
			"network": true,
		},
	}

	actions, _, err := ShellPlan(target)
	if err != nil {
		t.Fatalf("ShellPlan: %v", err)
	}

	a := actions[0]
	if !a.Network {
		t.Error("Network = false, want true")
	}
	if a.Env["KUBECONFIG"] != "/home/user/.kube/config" {
		t.Errorf("Env[KUBECONFIG] = %q, want /home/user/.kube/config", a.Env["KUBECONFIG"])
	}
}

func TestShellPlan_WithSources(t *testing.T) {
	target := config.Target{
		Name:      "//deploy",
		Toolchain: "shell",
		Sources:   []string{"deploy.sh", "config.yaml"},
		Config: map[string]any{
			"command": []any{"./deploy.sh"},
		},
	}

	actions, _, err := ShellPlan(target)
	if err != nil {
		t.Fatalf("ShellPlan: %v", err)
	}

	a := actions[0]
	if len(a.Inputs) != 2 {
		t.Errorf("got %d inputs, want 2", len(a.Inputs))
	}
	if a.Inputs["deploy.sh"] != "deploy.sh" {
		t.Errorf("missing deploy.sh input")
	}
}

func TestShellPlan_WithOutputs(t *testing.T) {
	target := config.Target{
		Name:      "//build",
		Toolchain: "shell",
		Config: map[string]any{
			"command": []any{"make", "build"},
			"outputs": []any{"dist/app"},
			"impure":  false,
		},
	}

	actions, _, err := ShellPlan(target)
	if err != nil {
		t.Fatalf("ShellPlan: %v", err)
	}

	a := actions[0]
	if len(a.Outputs) != 1 || a.Outputs[0] != "dist/app" {
		t.Errorf("Outputs = %v, want [dist/app]", a.Outputs)
	}
	if a.Impure {
		t.Error("Impure = true, want false (explicitly set)")
	}
}

func TestShellPlan_MissingCommand(t *testing.T) {
	target := config.Target{
		Name:      "//bad",
		Toolchain: "shell",
		Config:    map[string]any{},
	}

	_, _, err := ShellPlan(target)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestShellPlan_NilConfig(t *testing.T) {
	target := config.Target{
		Name:      "//bad",
		Toolchain: "shell",
	}

	_, _, err := ShellPlan(target)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestShellPlan_StringCommandRejected(t *testing.T) {
	target := config.Target{
		Name:      "//bad",
		Toolchain: "shell",
		Config: map[string]any{
			"command": "echo hello", // string, not array
		},
	}

	_, _, err := ShellPlan(target)
	if err == nil {
		t.Fatal("expected error for string command (must be []string)")
	}
}
