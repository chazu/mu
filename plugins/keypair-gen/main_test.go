package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/plugins/internal/plugintest"
	"github.com/chazu/mu/sdk/muplugin"
)

// TestParityWithBB replays the request script the retired plugin.bb
// answered (captured in testdata/parity_bb_responses.ndjson) and requires
// semantically identical responses. The one intentional deviation is the
// action env: bb's "env": {} (clean environment) is expressed as an
// explicit minimal PATH, because the SDK omits empty maps and an absent env
// would let this impure action inherit mu's environment.
func TestParityWithBB(t *testing.T) {
	plugintest.Parity(t, newPlugin(),
		filepath.Join("testdata", "parity_requests.ndjson"),
		filepath.Join("testdata", "parity_bb_responses.ndjson"),
		func(i int, golden map[string]any) {
			actions, _ := golden["actions"].([]any)
			for _, a := range actions {
				action := a.(map[string]any)
				if env, ok := action["env"].(map[string]any); !ok || len(env) != 0 {
					t.Fatalf("golden %d: expected bb action env {}, got %v", i+1, action["env"])
				}
				action["env"] = map[string]any{"PATH": actionEnv["PATH"]}
			}
		})
}

func planFor(t *testing.T, target muplugin.TargetInfo) muplugin.PlanResponse {
	t.Helper()
	var resp muplugin.PlanResponse
	req := muplugin.Request{Method: "plan", Target: &target}
	if err := muplugin.ExchangeInto(context.Background(), newPlugin(), req, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPlanSealedOutputContract(t *testing.T) {
	resp := planFor(t, muplugin.TargetInfo{
		Name:              "//keys/deploy",
		SealedOutputs:     map[string]string{"PRIVATE": "pass:raw:k", "PUBLIC": "pass:k.pub"},
		SealedOutputModes: map[string]string{"PRIVATE": "create_if_absent", "PUBLIC": "create_if_absent"},
	})
	if resp.Error != "" || len(resp.Actions) != 1 {
		t.Fatalf("plan = %+v", resp)
	}
	a := resp.Actions[0]
	if a.ID != "generate" || a.SealedOutputs["PRIVATE"] != "pass:raw:k" || a.SealedOutputModes["PUBLIC"] != "create_if_absent" {
		t.Fatalf("action = %+v", a)
	}
	if len(a.Env) != 1 || a.Env["PATH"] == "" {
		t.Fatalf("action env must be explicit (hermetic), got %v", a.Env)
	}

	resp = planFor(t, muplugin.TargetInfo{Name: "//k", SealedOutputs: map[string]string{"PRIVATE": "x:y", "OTHER": "x:z"}})
	if !strings.Contains(resp.Error, "must be exactly") {
		t.Fatalf("wrong key set: error = %q", resp.Error)
	}
}

// TestGeneratedScriptProducesKeypair runs the planned action the way the
// executor would (its env plus MU_SEALED_OUT_DIR) and checks both halves
// land in the sealed-output dir with mode 0600.
func TestGeneratedScriptProducesKeypair(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	for _, cfg := range []map[string]any{{}, {"type": "ecdsa", "curve": "P-384", "comment": "o'brien@host"}} {
		resp := planFor(t, muplugin.TargetInfo{
			Name:          "//keys/test",
			Config:        cfg,
			SealedOutputs: map[string]string{"PRIVATE": "x:priv", "PUBLIC": "x:pub"},
		})
		if resp.Error != "" {
			t.Fatal(resp.Error)
		}
		a := resp.Actions[0]
		outDir := t.TempDir()
		cmd := exec.Command(a.Command[0], a.Command[1:]...)
		cmd.Env = []string{"MU_SEALED_OUT_DIR=" + outDir}
		for k, v := range a.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("config %v: script failed: %v\n%s", cfg, err, out)
		}
		for _, name := range []string{"PRIVATE", "PUBLIC"} {
			info, err := os.Stat(filepath.Join(outDir, name))
			if err != nil {
				t.Fatalf("config %v: %s not written: %v", cfg, name, err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("config %v: %s mode = %v", cfg, name, info.Mode().Perm())
			}
		}
		pub, _ := os.ReadFile(filepath.Join(outDir, "PUBLIC"))
		wantPrefix := "ssh-ed25519 "
		if cfg["type"] == "ecdsa" {
			wantPrefix = "ecdsa-sha2-nistp384 "
		}
		if !strings.HasPrefix(string(pub), wantPrefix) {
			t.Fatalf("config %v: public key = %q", cfg, pub)
		}
		if c, ok := cfg["comment"].(string); ok && !strings.Contains(string(pub), c) {
			t.Fatalf("config %v: comment missing from public key %q", cfg, pub)
		}
	}
}
