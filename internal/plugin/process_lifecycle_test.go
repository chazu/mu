package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLifecycleHelperProcess(t *testing.T) {
	switch os.Getenv("MU_LIFECYCLE_HELPER") {
	case "no-read":
		for {
			time.Sleep(time.Hour)
		}
	case "no-response":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

func startLifecycleHelper(t *testing.T, mode string) *Process {
	t.Helper()
	t.Setenv("MU_LIFECYCLE_HELPER", mode)
	p, err := StartProcess("helper", []string{os.Args[0], "-test.run=^TestLifecycleHelperProcess$"}, ".", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestCancellationCoversBlockedRequestWrite(t *testing.T) {
	p := startLifecycleHelper(t, "no-read")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.StoreSecret(ctx, "ref", strings.Repeat("private", 1024*1024), "create") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked write: %v", err)
		}
	case <-time.After(3 * time.Second):
		p.abort()
		<-done
		t.Fatal("blocked stdin ignored request deadline")
	}
	select {
	case <-p.waitDone:
	default:
		t.Fatal("cancelled child was not reaped")
	}
}

func TestQueuedRequestDeadlineDoesNotInterruptActiveRequest(t *testing.T) {
	p := startLifecycleHelper(t, "no-response")
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first := make(chan error, 1)
	go func() { _, err := p.Discover(firstCtx); first <- err }()
	// Wait until the first exchange owns the gate.
	deadline := time.Now().Add(time.Second)
	for len(p.gate) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("first request did not acquire gate")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := p.Discover(ctx); second <- err }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued deadline: %v", err)
		}
	case <-time.After(time.Second):
		cancelFirst()
		<-first
		<-second
		t.Fatal("queued request ignored its deadline")
	}
	select {
	case <-p.closed:
		t.Fatal("queued timeout killed active request")
	default:
	}
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation: %v", err)
	}
}

func TestCloseJoinsExchangeAndIsConcurrentSafe(t *testing.T) {
	p := startLifecycleHelper(t, "no-response")
	done := make(chan error, 1)
	go func() { _, err := p.Discover(context.Background()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(p.gate) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("request did not acquire gate")
		}
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() { p.Close() })
	}
	closed := make(chan struct{})
	go func() { wg.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		p.abort()
		t.Fatal("Close did not join")
	}
	if err := <-done; err == nil {
		t.Fatal("active request survived Close")
	}
	if _, err := p.Discover(context.Background()); err == nil {
		t.Fatal("request accepted after Close")
	}
	select {
	case <-p.waitDone:
	default:
		t.Fatal("Close left child unreaped")
	}
	select {
	case <-p.stderrDone:
	default:
		t.Fatal("Close left stderr reader")
	}
}

func TestCloseKillsIdleUnresponsivePlugin(t *testing.T) {
	p := startLifecycleHelper(t, "no-read")
	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		p.abort()
		t.Fatal("idle Close hung")
	}
}
