//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancellationStopsOrdinaryDescendants(t *testing.T) {
	dir := t.TempDir()
	p, err := StartProcess("descendants", []string{"sh", "-c", "touch ready; sleep 0.5; touch survived; sleep 30"}, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			p.abort()
			t.Fatal("plugin did not start")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Discover(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "survived")); !os.IsNotExist(err) {
		t.Fatalf("descendant survived plugin cancellation: %v", err)
	}
}
