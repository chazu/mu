package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// DefaultDiscoverTimeout is the default timeout for discover requests.
const DefaultDiscoverTimeout = 10 * time.Second

// DefaultPlanTimeout is the default timeout for plan requests.
const DefaultPlanTimeout = 5 * time.Minute

// Process manages a single plugin subprocess communicating via NDJSON.
type Process struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	stderr  bytes.Buffer
	mu      sync.Mutex // serializes request/response pairs
}

// StartProcess spawns a plugin process with the given command.
// The command's working directory is set to projectRoot.
func StartProcess(name string, command []string, projectRoot string) (*Process, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("plugin %q: empty command", name)
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = projectRoot

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %q: stdin pipe: %w", name, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("plugin %q: stdout pipe: %w", name, err)
	}

	p := &Process{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		scanner: bufio.NewScanner(stdout),
	}
	cmd.Stderr = &p.stderr

	// Allow large responses (default 64KB may be too small for big action graphs).
	p.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin %q: start: %w", name, err)
	}

	return p, nil
}

// send writes a JSON request line and reads a JSON response line.
// It holds the mutex to ensure request/response pairs aren't interleaved.
func (p *Process) send(ctx context.Context, req Request, resp any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if process is still alive before sending.
	if p.cmd.ProcessState != nil {
		return fmt.Errorf("plugin %q: process exited (code %d): %s",
			p.name, p.cmd.ProcessState.ExitCode(), p.stderr.String())
	}

	// Encode request as a single JSON line.
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("plugin %q: marshal request: %w", p.name, err)
	}
	line = append(line, '\n')

	if _, err := p.stdin.Write(line); err != nil {
		return fmt.Errorf("plugin %q: write request: %w", p.name, err)
	}

	// Read response line with timeout via context.
	type scanResult struct {
		line []byte
		err  error
	}
	ch := make(chan scanResult, 1)
	go func() {
		if p.scanner.Scan() {
			// Copy the bytes since scanner reuses the buffer.
			b := make([]byte, len(p.scanner.Bytes()))
			copy(b, p.scanner.Bytes())
			ch <- scanResult{line: b}
		} else {
			ch <- scanResult{err: p.scanError()}
		}
	}()

	select {
	case <-ctx.Done():
		// Close stdin first so the child process sees EOF and can exit
		// cleanly, then kill the process to ensure it doesn't linger.
		p.stdin.Close()
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
		return fmt.Errorf("plugin %q: %w", p.name, ctx.Err())
	case result := <-ch:
		if result.err != nil {
			return result.err
		}
		if err := json.Unmarshal(result.line, resp); err != nil {
			return fmt.Errorf("plugin %q: unmarshal response: %w (raw: %s)", p.name, err, result.line)
		}
		return nil
	}
}

// scanError returns a descriptive error when the scanner fails.
func (p *Process) scanError() error {
	if err := p.scanner.Err(); err != nil {
		return fmt.Errorf("plugin %q: read response: %w", p.name, err)
	}
	// EOF — process closed stdout. Check if it crashed.
	stderr := p.stderr.String()
	if stderr != "" {
		return fmt.Errorf("plugin %q: process closed stdout: %s", p.name, stderr)
	}
	return fmt.Errorf("plugin %q: process closed stdout unexpectedly", p.name)
}

// Discover sends a discover request and validates the response.
func (p *Process) Discover(ctx context.Context) (*DiscoverResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultDiscoverTimeout)
	defer cancel()

	var resp DiscoverResponse
	if err := p.send(ctx, NewDiscoverRequest(), &resp); err != nil {
		return nil, err
	}

	if resp.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("plugin %q: protocol version mismatch: got %d, want %d",
			p.name, resp.ProtocolVersion, ProtocolVersion)
	}

	return &resp, nil
}

// Plan sends a plan request and returns the response.
func (p *Process) Plan(ctx context.Context, target TargetInfo, deps []DepInfo, toolchainArtifacts map[string]string) (*PlanResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultPlanTimeout)
	defer cancel()

	var resp PlanResponse
	req := NewPlanRequest(target, deps, toolchainArtifacts)
	if err := p.send(ctx, req, &resp); err != nil {
		return nil, err
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("plugin %q: plan error: %s", p.name, resp.Error)
	}

	return &resp, nil
}

// DefaultResolveSecretTimeout is the default timeout for resolve_secret requests.
// Kept short since secret resolution should be fast (local keyring, vault call, etc).
const DefaultResolveSecretTimeout = 30 * time.Second

// DefaultObserveTimeout is the default timeout for observe requests.
const DefaultObserveTimeout = 5 * time.Minute

// Observe sends an observe request and returns the response.
// If the plugin returns an error (e.g., "unknown method"), this is returned
// as a normal error — the Manager handles fallback to {State: "unknown"}.
func (p *Process) Observe(ctx context.Context, target TargetInfo, toolchainArtifacts map[string]string) (*ObserveResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultObserveTimeout)
	defer cancel()

	var resp ObserveResponse
	req := NewObserveRequest(target, toolchainArtifacts)
	if err := p.send(ctx, req, &resp); err != nil {
		return nil, err
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("plugin %q: observe error: %s", p.name, resp.Error)
	}

	return &resp, nil
}

// ResolveSecret sends a resolve_secret request and returns the resolved value.
// The returned value must never be logged, cached, or stored in CAS.
func (p *Process) ResolveSecret(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultResolveSecretTimeout)
	defer cancel()

	var resp ResolveSecretResponse
	if err := p.send(ctx, NewResolveSecretRequest(ref), &resp); err != nil {
		return "", err
	}

	if resp.Error != "" {
		return "", fmt.Errorf("plugin %q: resolve_secret error: %s", p.name, resp.Error)
	}

	return resp.Value, nil
}

// Close gracefully shuts down the plugin process.
// Closes stdin and waits for the process to exit.
func (p *Process) Close() error {
	p.stdin.Close()
	err := p.cmd.Wait()
	if err != nil {
		// Exit code != 0 after stdin close is expected for some plugins.
		// Only return error if there's useful stderr.
		if stderr := p.stderr.String(); stderr != "" {
			return fmt.Errorf("plugin %q: exit: %w: %s", p.name, err, stderr)
		}
	}
	return nil
}

// Name returns the plugin's name (from the config, not from discover).
func (p *Process) Name() string {
	return p.name
}
