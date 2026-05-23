package muplugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chau/mu/sdk/muplugin"
)

func TestDiscoverDerivesCapabilities(t *testing.T) {
	p := &muplugin.Plugin{
		Name:     "hello",
		Version:  "0.1.0",
		Produces: []string{"text"},
		Plan: func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
			return muplugin.PlanResponse{}, nil
		},
	}
	var resp muplugin.DiscoverResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewDiscoverRequest(), &resp); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Name != "hello" || resp.Version != "0.1.0" {
		t.Fatalf("identity wrong: %+v", resp)
	}
	if resp.ProtocolVersion != muplugin.ProtocolVersion {
		t.Fatalf("protocol_version %d, want %d", resp.ProtocolVersion, muplugin.ProtocolVersion)
	}
	wantCaps := map[string]bool{"discover": true, "plan": true}
	if len(resp.Capabilities) != len(wantCaps) {
		t.Fatalf("capabilities %v, want %v", resp.Capabilities, wantCaps)
	}
	for _, c := range resp.Capabilities {
		if !wantCaps[c] {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}

func TestDiscoverAddsOptionalCapabilities(t *testing.T) {
	p := &muplugin.Plugin{
		Name:    "secrets",
		Version: "0.1.0",
		Plan: func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
			return muplugin.PlanResponse{}, nil
		},
		Observe:       func(ctx context.Context, req muplugin.ObserveRequest) (muplugin.ObserveResponse, error) { return muplugin.ObserveResponse{}, nil },
		ResolveSecret: func(ctx context.Context, ref string) (string, error) { return "", nil },
		StoreSecret:   func(ctx context.Context, req muplugin.StoreSecretRequest) error { return nil },
		Advise:        func(ctx context.Context, req muplugin.AdviseRequest) error { return nil },
		AdvisePhases:  []string{"after-build"},
	}
	var resp muplugin.DiscoverResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewDiscoverRequest(), &resp); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	want := []string{"discover", "plan", "observe", "resolve_secret", "store_secret", "advise"}
	if len(resp.Capabilities) != len(want) {
		t.Fatalf("capabilities %v, want %v", resp.Capabilities, want)
	}
	for i, c := range want {
		if resp.Capabilities[i] != c {
			t.Fatalf("capability[%d] = %q, want %q", i, resp.Capabilities[i], c)
		}
	}
	if len(resp.AdvisePhases) != 1 || resp.AdvisePhases[0] != "after-build" {
		t.Fatalf("advise_phases %v", resp.AdvisePhases)
	}
}

func TestPlanRoundTrip(t *testing.T) {
	p := &muplugin.Plugin{
		Name:    "echo",
		Version: "0.1.0",
		Plan: func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
			out, _ := req.Target.Config["out"].(string)
			if out == "" {
				out = "out.txt"
			}
			return muplugin.PlanResponse{
				Actions: []muplugin.ActionSpec{{
					ID:      "run",
					Command: []string{"echo", req.Target.Name},
					Outputs: []string{out},
				}},
				Outputs: map[string]string{"text": out},
			}, nil
		},
	}
	req := muplugin.NewPlanRequest(muplugin.TargetInfo{
		Name:      "//foo",
		Toolchain: "echo",
		Config:    map[string]any{"out": "hello.txt"},
	}, nil, nil)
	var resp muplugin.PlanResponse
	if err := muplugin.ExchangeInto(t.Context(), p, req, &resp); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("plan returned error: %s", resp.Error)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].ID != "run" {
		t.Fatalf("actions = %+v", resp.Actions)
	}
	if resp.Outputs["text"] != "hello.txt" {
		t.Fatalf("outputs = %+v", resp.Outputs)
	}
}

func TestPlanErrorSurfacedInResponse(t *testing.T) {
	p := &muplugin.Plugin{
		Name:    "boom",
		Version: "0.1.0",
		Plan: func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
			return muplugin.PlanResponse{}, errors.New("kaboom")
		},
	}
	req := muplugin.NewPlanRequest(muplugin.TargetInfo{Name: "//x", Toolchain: "boom"}, nil, nil)
	var resp muplugin.PlanResponse
	if err := muplugin.ExchangeInto(t.Context(), p, req, &resp); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Error != "kaboom" {
		t.Fatalf("error %q, want %q", resp.Error, "kaboom")
	}
}

func TestUnsupportedCapabilityReturnsError(t *testing.T) {
	p := &muplugin.Plugin{
		Name:    "minimal",
		Version: "0.1.0",
		Plan:    func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) { return muplugin.PlanResponse{}, nil },
	}
	var resp muplugin.ObserveResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewObserveRequest(muplugin.TargetInfo{Name: "//x"}, nil, nil), &resp); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("expected capability-unsupported error")
	}
}

type fakeBackend struct {
	store map[string]string
}

func (b *fakeBackend) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := b.store[ref]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func (b *fakeBackend) Store(_ context.Context, ref, value, _ string) error {
	if b.store == nil {
		b.store = map[string]string{}
	}
	b.store[ref] = value
	return nil
}

func TestSecretPluginExposesBackend(t *testing.T) {
	b := &fakeBackend{store: map[string]string{"k": "v"}}
	p := muplugin.SecretPlugin("pass", "0.1.0", b)

	var got muplugin.ResolveSecretResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewResolveSecretRequest("k"), &got); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if got.Value != "v" {
		t.Fatalf("value = %q, want %q", got.Value, "v")
	}

	var stored muplugin.StoreSecretResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewStoreSecretRequest("k2", "v2", muplugin.StoreSecretModeOverwrite), &stored); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if stored.Error != "" {
		t.Fatalf("store error: %s", stored.Error)
	}
	if b.store["k2"] != "v2" {
		t.Fatalf("backend did not receive store; got %+v", b.store)
	}

	var disc muplugin.DiscoverResponse
	if err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewDiscoverRequest(), &disc); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	want := map[string]bool{"discover": true, "plan": true, "resolve_secret": true, "store_secret": true}
	if len(disc.Capabilities) != len(want) {
		t.Fatalf("capabilities %v want %v", disc.Capabilities, want)
	}
	for _, c := range disc.Capabilities {
		if !want[c] {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}
