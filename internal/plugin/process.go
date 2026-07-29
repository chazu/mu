package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultDiscoverTimeout is the default timeout for discover requests.
const DefaultDiscoverTimeout = 10 * time.Second

// DefaultPlanTimeout is the default timeout for plan requests.
const DefaultPlanTimeout = 5 * time.Minute

// Process manages a single plugin subprocess communicating via NDJSON.
//
// Stderr semantics: plugins may write free-form line-delimited text to
// stderr for human debugging. The coordinator captures each line into
// a fixed-size ring buffer (tagged "[name] "), and optionally mirrors
// it to a shared live sink (Manager.SetVerbose). stdout is reserved
// for NDJSON protocol traffic and is never parsed as free text.
type Process struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	logs    *logBuffer

	// liveSink, if non-nil, receives every tagged stderr line as it
	// arrives. Writes are serialized through liveSinkMu so lines from
	// multiple Processes never interleave mid-line.
	liveSink   io.Writer
	liveSinkMu *sync.Mutex

	stderrDone chan struct{} // closed when the stderr pump exits

	mu sync.Mutex // serializes request/response pairs on stdout
}

// ProcessOptions configures optional behaviour for a spawned plugin.
type ProcessOptions struct {
	// LiveSink, if non-nil, receives every tagged stderr line as soon
	// as it arrives. Intended for --verbose CLI forwarding.
	LiveSink io.Writer
	// LiveSinkMu protects shared LiveSink writes across multiple
	// processes. Required iff LiveSink is non-nil.
	LiveSinkMu *sync.Mutex
	// LogCapacity overrides the default ring size (DefaultLogRingSize).
	LogCapacity int
}

// StartProcess spawns a plugin process with the given command.
// If workDir is non-empty, it is used as the process working directory;
// otherwise projectRoot is used. Bundled plugins use workDir so they
// can find sibling files relative to the extracted bundle root.
func StartProcess(name string, command []string, projectRoot string, workDir string) (*Process, error) {
	return StartProcessWithOptions(name, command, projectRoot, workDir, ProcessOptions{})
}

// StartProcessWithOptions spawns a plugin process with optional live
// stderr forwarding and ring-capacity override.
func StartProcessWithOptions(name string, command []string, projectRoot string, workDir string, opts ProcessOptions) (*Process, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("plugin %q: empty command", name)
	}

	cmd := exec.Command(command[0], command[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	} else {
		cmd.Dir = projectRoot
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %q: stdin pipe: %w", name, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("plugin %q: stdout pipe: %w", name, err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("plugin %q: stderr pipe: %w", name, err)
	}

	p := &Process{
		name:       name,
		cmd:        cmd,
		stdin:      stdin,
		scanner:    bufio.NewScanner(stdout),
		logs:       newLogBuffer(opts.LogCapacity),
		liveSink:   opts.LiveSink,
		liveSinkMu: opts.LiveSinkMu,
		stderrDone: make(chan struct{}),
	}

	// Allow large responses (default 64KB may be too small for big action graphs).
	p.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin %q: start: %w", name, err)
	}

	go p.pumpStderr(stderr)

	return p, nil
}

// pumpStderr consumes the plugin's stderr pipe until EOF, tagging each
// line and storing it in the ring buffer. When a live sink is
// configured, the tagged line is also written through immediately. The
// goroutine exits when the pipe returns EOF (typically on child exit);
// stderrDone is then closed so Close() can join.
func (p *Process) pumpStderr(stderr io.ReadCloser) {
	defer close(p.stderrDone)
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		tagged := "[" + p.name + "] " + scanner.Text()
		p.logs.Append(tagged)
		if p.liveSink != nil {
			p.writeLive(tagged)
		}
	}
	if err := scanner.Err(); err != nil {
		synthetic := fmt.Sprintf("[%s] <stderr read error: %v>", p.name, err)
		p.logs.Append(synthetic)
		if p.liveSink != nil {
			p.writeLive(synthetic)
		}
	}
}

func (p *Process) writeLive(line string) {
	if p.liveSinkMu != nil {
		p.liveSinkMu.Lock()
		defer p.liveSinkMu.Unlock()
	}
	_, _ = io.WriteString(p.liveSink, line+"\n")
}

// logsSnapshot returns a joined string of the ring contents, oldest
// first, for error wrapping. Returns "" when empty.
func (p *Process) logsSnapshot() string {
	snap := p.logs.Snapshot()
	if len(snap) == 0 {
		return ""
	}
	return strings.Join(snap, "\n")
}

// Logs returns a fresh copy of the ring contents (tagged strings,
// oldest first). Safe to call before or after Close.
func (p *Process) Logs() []string { return p.logs.Snapshot() }

// send writes a JSON request line and reads a JSON response line.
// It holds the mutex to ensure request/response pairs aren't interleaved.
func (p *Process) send(ctx context.Context, req Request, resp any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if process is still alive before sending.
	if p.cmd.ProcessState != nil {
		return fmt.Errorf("plugin %q: process exited (code %d): %s",
			p.name, p.cmd.ProcessState.ExitCode(), p.logsSnapshot())
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
	stderr := p.logsSnapshot()
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
	if err := ValidateDiscoverResponse(resp); err != nil {
		return nil, fmt.Errorf("plugin %q: invalid discover response: %w", p.name, err)
	}

	return &resp, nil
}

// ValidateDiscoverResponse checks the structural parts of a discover
// response that every plugin must satisfy. A missing capabilities array is
// accepted for compatibility with pre-capability plugins; when present, it
// must be explicit, unique, and include discover.
func ValidateDiscoverResponse(resp DiscoverResponse) error {
	if strings.TrimSpace(resp.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(resp.Version) == "" {
		return fmt.Errorf("version is required")
	}

	if len(resp.Capabilities) > 0 {
		seen := make(map[string]struct{}, len(resp.Capabilities))
		for _, capability := range resp.Capabilities {
			if _, ok := validCapabilities[capability]; !ok {
				return fmt.Errorf("unknown capability %q", capability)
			}
			if _, ok := seen[capability]; ok {
				return fmt.Errorf("duplicate capability %q", capability)
			}
			seen[capability] = struct{}{}
		}
		if _, ok := seen["discover"]; !ok {
			return fmt.Errorf("capabilities must include discover")
		}
	}

	if err := validateSchemaRef(resp.OutputSchema); err != nil {
		return fmt.Errorf("output_schema: %w", err)
	}
	seenResourceTypes := make(map[string]struct{}, len(resp.OutputSchemas))
	for i := range resp.OutputSchemas {
		schema := &resp.OutputSchemas[i]
		if err := validateSchemaRef(schema); err != nil {
			return fmt.Errorf("output_schemas[%d]: %w", i, err)
		}
		if schema.ResourceType == "" {
			continue
		}
		if _, ok := seenResourceTypes[schema.ResourceType]; ok {
			return fmt.Errorf("output_schemas: duplicate resource_type %q", schema.ResourceType)
		}
		seenResourceTypes[schema.ResourceType] = struct{}{}
	}

	return nil
}

var validCapabilities = map[string]struct{}{
	"discover":       {},
	"plan":           {},
	"observe":        {},
	"resolve_secret": {},
	"store_secret":   {},
	"advise":         {},
}

func validateSchemaRef(schema *SchemaRef) error {
	if schema == nil {
		return nil
	}
	if strings.TrimSpace(schema.Module) == "" {
		return fmt.Errorf("module is required")
	}
	if strings.TrimSpace(schema.Version) == "" {
		return fmt.Errorf("version is required")
	}
	return nil
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

// DefaultStoreSecretTimeout is the default timeout for store_secret requests.
// Symmetric with resolve_secret — backends should respond quickly.
const DefaultStoreSecretTimeout = 30 * time.Second

// DefaultObserveTimeout is the default timeout for observe requests.
const DefaultObserveTimeout = 5 * time.Minute

// Observe sends an observe request and returns the response.
// If the plugin returns an error (e.g., "unknown method"), this is returned
// as a normal error — the Manager handles fallback to an empty response.
func (p *Process) Observe(ctx context.Context, target TargetInfo, toolchainArtifacts map[string]string, secrets map[string]string) (*ObserveResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultObserveTimeout)
	defer cancel()

	var resp ObserveResponse
	req := NewObserveRequest(target, toolchainArtifacts, secrets)
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

// StoreSecret sends a store_secret request to persist a value at the
// given ref. The value bytes must never be logged or cached by the
// caller; this method does not log them.
func (p *Process) StoreSecret(ctx context.Context, ref, value, mode string) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultStoreSecretTimeout)
	defer cancel()

	var resp StoreSecretResponse
	if err := p.send(ctx, NewStoreSecretRequest(ref, value, mode), &resp); err != nil {
		return err
	}

	if resp.Error != "" {
		return fmt.Errorf("plugin %q: store_secret error: %s", p.name, resp.Error)
	}

	return nil
}

// Advise sends an advise request and returns the response.
// Advice errors are non-fatal to the build — callers should log but not fail.
func (p *Process) Advise(ctx context.Context, phase string, manifest any, advCtx *AdviseContext, cfg map[string]any, secrets map[string]string) (*AdviseResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultAdviseTimeout)
	defer cancel()

	var resp AdviseResponse
	req := NewAdviseRequest(phase, manifest, advCtx, cfg, secrets)
	if err := p.send(ctx, req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// Close gracefully shuts down the plugin process.
// Closes stdin and waits for the process and the stderr pump to exit.
func (p *Process) Close() error {
	p.stdin.Close()
	err := p.cmd.Wait()
	// Drain the stderr pump — cmd.Wait() signals process exit but the
	// pump may still be flushing buffered lines before EOF is observed.
	<-p.stderrDone
	if err != nil {
		// Exit code != 0 after stdin close is expected for some plugins.
		// Only return error if there's useful stderr.
		if stderr := p.logsSnapshot(); stderr != "" {
			return fmt.Errorf("plugin %q: exit: %w: %s", p.name, err, stderr)
		}
	}
	return nil
}

// Name returns the plugin's name (from the config, not from discover).
func (p *Process) Name() string {
	return p.name
}
