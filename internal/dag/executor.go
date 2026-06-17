package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/pithvm"
	"github.com/chazu/mu/internal/sandbox"
	"github.com/chazu/pith"
)

// ActionStatus represents the result of executing a single action.
type ActionStatus struct {
	ID       string
	Cached   bool
	ExitCode int
	Err      error
	Outputs  map[string]cas.Digest // output name -> content digest

	// Attempts is the number of times the action was run. 0 if served
	// from cache, 1 if executed successfully on the first try, >1 if
	// retries happened. Only meaningful for actions with Retries > 0.
	Attempts int
}

// ExecuteResult holds the outcome of a full DAG execution.
type ExecuteResult struct {
	Completed []ActionStatus
	Failed    []ActionStatus
	Cancelled []string // action IDs cancelled due to upstream failure
}

// SealedOutputWriter persists a captured sealed-output value to its
// destination. Implementations route the call to the appropriate
// secret-provider plugin based on the ref's scheme. The mode selects
// the store_secret semantics ("create", "overwrite", or
// "create_if_absent"); an empty mode means "overwrite". The value
// bytes must never be logged or cached by the implementation.
type SealedOutputWriter func(ctx context.Context, ref, value, mode string) error

// Executor runs a DAG of actions with parallel scheduling and CAS caching.
type Executor struct {
	Store           cas.Store
	Workers         int                          // 0 means runtime.NumCPU()
	ResolvedSecrets map[string]map[string]string // actionID → envName → secret value (never persisted)
	// SubprocessStdout overrides where action stdout is written. Nil
	// means os.Stdout. Set to os.Stderr when the caller is using
	// stdout for structured output (e.g. `mu build --emit-manifest`).
	SubprocessStdout io.Writer
	// SealedOutputWriter, if non-nil, is invoked once per (name, ref)
	// after a sealed-output action exits successfully. If nil, an
	// action with non-empty SealedOutputs fails — the runner cannot
	// silently drop captured secrets.
	SealedOutputWriter SealedOutputWriter

	// completedOutputs tracks output file paths by target prefix for
	// completed actions, enabling pith VM getOutput closures.
	// Protected by outputsMu since the main goroutine writes and
	// worker goroutines read concurrently.
	completedOutputs map[string]map[string]string // target -> outputName -> filePath

	// pithResults stores pith VM stack results by target prefix.
	// Transform actions and body actions write their result here so
	// downstream actions can access it via target/output under the
	// "_result" key without requiring file-based output declarations.
	pithResults map[string]any // target -> stack result

	outputsMu sync.RWMutex
}

// subprocessStdout returns the configured stdout target for action
// subprocesses, defaulting to os.Stdout.
func (e *Executor) subprocessStdout() io.Writer {
	if e.SubprocessStdout != nil {
		return e.SubprocessStdout
	}
	return os.Stdout
}

// Execute runs all actions in the graph, respecting dependencies.
// On failure: cancels transitive dependents, continues independent subgraphs.
func (e *Executor) Execute(ctx context.Context, g *Graph) (*ExecuteResult, error) {
	levels, err := TopoSort(g)
	if err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}
	if len(levels) == 0 {
		return &ExecuteResult{}, nil
	}

	workers := e.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Initialize completed outputs tracking for pith VM getOutput.
	e.completedOutputs = make(map[string]map[string]string)
	e.pithResults = make(map[string]any)

	// Track state for each action.
	var mu sync.Mutex
	inDegree := make(map[string]int, g.Len())
	for _, id := range g.order {
		inDegree[id] = len(g.deps[id])
	}
	cancelled := make(map[string]bool)
	result := &ExecuteResult{}

	// ready receives actions whose dependencies are all satisfied.
	ready := make(chan *Action, g.Len())
	// done receives the result of each action execution.
	done := make(chan ActionStatus, g.Len())

	// Seed with actions that have no dependencies.
	pending := 0
	for _, a := range levels[0] {
		ready <- a
		pending++
	}

	// Spawn worker pool.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range ready {
				status := e.executeAction(ctx, a)
				done <- status
			}
		}()
	}

	// Process results and unblock dependents.
	for pending > 0 {
		select {
		case <-ctx.Done():
			close(ready)
			wg.Wait()
			return result, ctx.Err()
		case status := <-done:
			pending--

			if status.Err != nil {
				mu.Lock()
				result.Failed = append(result.Failed, status)
				// Cancel all transitive dependents of the failed action.
				for _, depID := range g.TransitiveDependents(status.ID) {
					if !cancelled[depID] {
						cancelled[depID] = true
						// If this dependent was pending (not yet queued), account for it.
						if inDegree[depID] > 0 {
							result.Cancelled = append(result.Cancelled, depID)
						}
					}
				}
				mu.Unlock()
			} else {
				mu.Lock()
				result.Completed = append(result.Completed, status)
				mu.Unlock()

				// Record completed outputs by target prefix for pith VM getOutput.
				if idx := strings.LastIndex(status.ID, ":"); idx >= 0 {
					targetName := status.ID[:idx]
					e.outputsMu.Lock()
					if e.completedOutputs[targetName] == nil {
						e.completedOutputs[targetName] = make(map[string]string)
					}
					for _, a := range g.Actions() {
						if a.ID == status.ID {
							for _, outPath := range a.Outputs {
								resolved := outPath
								if !filepath.IsAbs(outPath) && a.WorkDir != "" {
									resolved = filepath.Join(a.WorkDir, outPath)
								}
								e.completedOutputs[targetName][outPath] = resolved
							}
							break
						}
					}
					e.outputsMu.Unlock()
				}
			}

			// Unblock dependents.
			mu.Lock()
			for _, depID := range g.Dependents(status.ID) {
				if cancelled[depID] {
					continue
				}
				inDegree[depID]--
				if inDegree[depID] == 0 {
					pending++
					ready <- g.Action(depID)
				}
			}
			mu.Unlock()
		}
	}

	close(ready)
	wg.Wait()

	return result, nil
}

// executeAction runs a single action: check cache, execute if miss, store results.
// Impure actions skip cache lookup and storage entirely.
func (e *Executor) executeAction(ctx context.Context, a *Action) ActionStatus {
	// Cache check — only for pure actions.
	if !a.Impure {
		key := ComputeActionKey(a)
		if e.Store != nil {
			cached, err := e.Store.GetActionResult(ctx, key)
			if err == nil && cached != nil {
				// Cache hit — restore outputs from CAS.
				if err := e.restoreOutputs(ctx, a, cached); err == nil {
					return ActionStatus{ID: a.ID, Cached: true, ExitCode: cached.ExitCode, Outputs: cached.Outputs}
				}
				// On restore failure, fall through to re-execute.
			}
		}
	}

	// Execute the command (or pith Body program).
	if len(a.Command) == 0 && len(a.Body) == 0 {
		return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q has no command or body", a.ID)}
	}

	// Merge resolved secrets into a copy of the env. We must not mutate a.Env
	// because ComputeActionKey is called again after execution for cache storage,
	// and secrets must never be part of the cache key.
	execEnv := a.Env
	secrets := e.ResolvedSecrets[a.ID]
	var sealedInDir string
	if len(secrets) > 0 {
		execEnv = make(map[string]string, len(a.Env)+len(secrets)+1)
		for k, v := range a.Env {
			execEnv[k] = v
		}
		// Apply per-name delivery modes. "env" (or empty) is the default
		// and exports the value verbatim; "file" writes to a 0600 temp
		// file under a per-action sealed-input directory and exports the
		// path. Sandbox-mode actions cannot use "file" because the temp
		// file lives outside the sandbox view.
		for k, v := range secrets {
			mode := a.SealedInputModes[k]
			switch mode {
			case "", "env":
				execEnv[k] = v
			case "file":
				if a.Toolchain != nil {
					return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: sealed_input %q uses mode=file, which is not supported in sandboxed (toolchain) actions", a.ID, k)}
				}
				if sealedInDir == "" {
					dir, err := os.MkdirTemp("", "mu-sealed-in-*")
					if err != nil {
						return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: create sealed-in dir: %w", a.ID, err)}
					}
					sealedInDir = dir
					defer os.RemoveAll(sealedInDir)
					if err := os.Chmod(sealedInDir, 0o700); err != nil {
						return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: chmod sealed-in dir: %w", a.ID, err)}
					}
				}
				path := filepath.Join(sealedInDir, k)
				if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
					return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: write sealed_input %q: %w", a.ID, k, err)}
				}
				execEnv[k] = path
			default:
				return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: sealed_input %q: unknown mode %q", a.ID, k, mode)}
			}
		}
	}

	// Sealed-output side channel: create a per-action temp directory and
	// expose its path via $MU_SEALED_OUT_DIR. The action writes each
	// declared name as a file there; we read them post-exec and route
	// through the configured writer. The directory is removed on
	// completion regardless of success, so failed values do not linger.
	var sealedOutDir string
	if len(a.SealedOutputs) > 0 {
		if e.SealedOutputWriter == nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: sealed_outputs declared but executor has no SealedOutputWriter", a.ID)}
		}
		dir, err := os.MkdirTemp("", "mu-sealed-out-*")
		if err != nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: create sealed-out dir: %w", a.ID, err)}
		}
		sealedOutDir = dir
		defer os.RemoveAll(sealedOutDir)
		// 0700 — only the running user can see it.
		if err := os.Chmod(sealedOutDir, 0o700); err != nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: chmod sealed-out dir: %w", a.ID, err)}
		}
		// Ensure execEnv is a fresh copy before we add MU_SEALED_OUT_DIR.
		if execEnv == nil || len(secrets) == 0 {
			execEnv = make(map[string]string, len(a.Env)+1)
			for k, v := range a.Env {
				execEnv[k] = v
			}
		}
		execEnv["MU_SEALED_OUT_DIR"] = sealedOutDir
	}

	// For bare-mode actions with relative output paths, create a per-action
	// output directory and expose it as $MU_OUT. After execution, outputs
	// are copied to WorkDir so dependents and cache storage can find them.
	var muOutDir string
	if a.Toolchain == nil && hasRelativeOutputs(a.Outputs) {
		dir, err := os.MkdirTemp("", "mu-out-*")
		if err != nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: create output dir: %w", a.ID, err)}
		}
		muOutDir = dir
		defer os.RemoveAll(muOutDir)
		if execEnv == nil {
			execEnv = make(map[string]string, len(a.Env)+1)
			for k, v := range a.Env {
				execEnv[k] = v
			}
		}
		execEnv["MU_OUT"] = muOutDir
	}

	exitCode, attempts, execErr := e.runWithTimeoutAndRetry(ctx, a, execEnv)

	if execErr != nil {
		return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q failed: %w", a.ID, execErr)}
	}

	// Copy outputs from MU_OUT staging dir to WorkDir so dependents can read them.
	if muOutDir != "" {
		for _, outRel := range a.Outputs {
			srcPath := filepath.Join(muOutDir, outRel)
			dstPath := filepath.Join(a.WorkDir, outRel)
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q: create output dir for %s: %w", a.ID, outRel, err)}
			}
			src, err := os.Open(srcPath)
			if err != nil {
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q: open output %s: %w", a.ID, outRel, err)}
			}
			if err := writeFile(dstPath, src); err != nil {
				src.Close()
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q: copy output %s: %w", a.ID, outRel, err)}
			}
			src.Close()
		}
	}

	// Capture sealed outputs post-exec. Each declared name must exist as
	// a file in the side-channel dir; routing happens before cache
	// storage so a routing failure aborts the action.
	if len(a.SealedOutputs) > 0 {
		if err := e.captureSealedOutputs(ctx, a, sealedOutDir); err != nil {
			return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: err}
		}
	}

	// Hash declared outputs and store in CAS — only for pure actions.
	actionResult := &cas.ActionResult{
		Outputs:  make(map[string]cas.Digest),
		ExitCode: exitCode,
	}

	if e.Store != nil && !a.Impure {
		for _, outPath := range a.Outputs {
			absPath := outPath
			if muOutDir != "" && !filepath.IsAbs(outPath) {
				absPath = filepath.Join(a.WorkDir, outPath)
			}
			dgst, err := e.storeOutput(ctx, absPath)
			if err != nil {
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Err: fmt.Errorf("action %q: storing output %q: %w", a.ID, outPath, err)}
			}
			actionResult.Outputs[outPath] = dgst
		}

		key := ComputeActionKey(a)
		if err := e.Store.PutActionResult(ctx, key, actionResult); err != nil {
			// Cache write failure is not fatal — warn but don't fail the action.
			fmt.Fprintf(os.Stderr, "mu: warning: cache write for action %q: %v\n", a.ID, err)
		}
	}

	return ActionStatus{ID: a.ID, ExitCode: exitCode, Outputs: actionResult.Outputs, Attempts: attempts}
}

// runWithTimeoutAndRetry executes the action with optional per-attempt
// timeout and optional retry. Retries apply only when Network is true;
// Retries on a non-network action are silently ignored. Parent-context
// cancellation (SIGINT) returns immediately.
//
// Returns (exitCode, attempts, err). attempts is 1 on success without
// retry. err is non-nil iff the final attempt failed OR the parent
// context was cancelled.
func (e *Executor) runWithTimeoutAndRetry(ctx context.Context, a *Action, env map[string]string) (int, int, error) {
	maxRetries := 0
	if a.Network && a.Retries > 0 {
		maxRetries = a.Retries
	}
	timeout := time.Duration(a.TimeoutS) * time.Second
	backoff := time.Duration(a.RetryBackoffMs) * time.Millisecond

	var lastExit int
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return lastExit, attempt, ctx.Err()
		}

		attemptCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}

		var exitCode int
		var err error
		if a.Toolchain != nil {
			exitCode, err = e.executeInSandbox(attemptCtx, a, env)
		} else if len(a.Body) > 0 {
			exitCode, err = e.executePithVM(attemptCtx, a, env)
		} else {
			exitCode, err = e.executeBare(attemptCtx, a, env)
		}

		if cancel != nil {
			cancel()
		}

		if err == nil {
			return exitCode, attempt + 1, nil
		}

		lastExit = exitCode
		lastErr = err

		// Don't retry on parent-context cancellation.
		if ctx.Err() != nil {
			return lastExit, attempt + 1, ctx.Err()
		}

		// If we have attempts remaining, back off then loop.
		if attempt < maxRetries && backoff > 0 {
			select {
			case <-ctx.Done():
				return lastExit, attempt + 1, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return lastExit, maxRetries + 1, lastErr
}

// executeBare runs a command directly on the host (no sandbox).
// Used for actions without a toolchain, preserving backward compatibility.
// The env parameter may include resolved secrets merged with the action's declared env.
func (e *Executor) executeBare(ctx context.Context, a *Action, env map[string]string) (int, error) {
	cmd := exec.CommandContext(ctx, a.Command[0], a.Command[1:]...)
	cmd.Dir = a.WorkDir
	cmd.Env = buildEnv(env)
	cmd.Stdout = e.subprocessStdout()
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return exitCode, err
}

// executePithVM runs a pith VM program instead of a shell command.
func (e *Executor) executePithVM(ctx context.Context, a *Action, env map[string]string) (int, error) {
	vm := pith.New(ctx)

	getOutput := func(targetName string) (map[string]any, error) {
		e.outputsMu.RLock()
		outputs := e.completedOutputs[targetName]
		pithResult := e.pithResults[targetName]
		e.outputsMu.RUnlock()

		result := make(map[string]any)
		for name, path := range outputs {
			data, err := os.ReadFile(path)
			if err != nil {
				result[name] = map[string]any{"path": path, "error": err.Error()}
				continue
			}
			var parsed any
			if json.Unmarshal(data, &parsed) == nil {
				result[name] = parsed
			} else {
				result[name] = string(data)
			}
		}
		if pithResult != nil {
			result["_result"] = pithResult
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("no outputs for target %q", targetName)
		}
		return result, nil
	}

	// sealedNames lets the secret/env words partition the namespace: secret/get
	// serves only these names; env/get refuses them. Names only — never values.
	sealedNames := make(map[string]bool, len(a.SealedInputs))
	for name := range a.SealedInputs {
		sealedNames[name] = true
	}

	// Expose WorkDir as a sanctioned file/write root for pith bodies, alongside
	// MU_SEALED_OUT_DIR / MU_OUT (added earlier in runAction). Copy first so we
	// never mutate the caller's env map.
	if a.WorkDir != "" && env["MU_WORK_DIR"] == "" {
		cp := make(map[string]string, len(env)+1)
		for k, v := range env {
			cp[k] = v
		}
		cp["MU_WORK_DIR"] = a.WorkDir
		env = cp
	}

	pithvm.RegisterExecDrivers(vm, env, sealedNames, getOutput, e.Store)
	if err := vm.Run(a.Body); err != nil {
		return 1, err
	}

	// Store stack result for downstream access via target/output "_result" key.
	if result, err := vm.Result(); err == nil && result != nil {
		if idx := strings.LastIndex(a.ID, ":"); idx >= 0 {
			targetName := a.ID[:idx]
			e.outputsMu.Lock()
			e.pithResults[targetName] = result
			e.outputsMu.Unlock()
		}
	}

	return 0, nil
}

// executeInSandbox runs a command in a hermetic sandbox environment.
// The toolchain artifacts are unpacked into the sandbox, sources are copied in,
// the command runs inside the sandbox, and outputs are copied back to WorkDir.
// The env parameter may include resolved secrets merged with the action's declared env.
func (e *Executor) executeInSandbox(ctx context.Context, a *Action, env map[string]string) (int, error) {
	sb, err := sandbox.New(e.Store)
	if err != nil {
		return -1, fmt.Errorf("create sandbox: %w", err)
	}
	defer sb.Cleanup()
	sb.Stdout = e.SubprocessStdout

	// Unpack toolchain into sandbox rootfs.
	if err := sb.UnpackToolchain(ctx, a.Toolchain); err != nil {
		return -1, fmt.Errorf("unpack toolchain: %w", err)
	}

	// Copy sources into sandbox work directory.
	if len(a.Sources) > 0 && a.WorkDir != "" {
		if err := sb.CopySources(a.WorkDir, a.Sources); err != nil {
			return -1, fmt.Errorf("copy sources: %w", err)
		}
	}

	// Execute.
	exitCode, err := sb.Exec(ctx, a.Command, env, a.Network)
	if err != nil {
		return exitCode, err
	}

	// Copy declared outputs back from sandbox to the original WorkDir.
	for _, outRel := range a.Outputs {
		sbOut := sb.OutputPath(outRel)
		hostOut := filepath.Join(a.WorkDir, outRel)
		if err := os.MkdirAll(filepath.Dir(hostOut), 0o755); err != nil {
			return exitCode, fmt.Errorf("create output dir for %s: %w", outRel, err)
		}
		src, err := os.Open(sbOut)
		if err != nil {
			return exitCode, fmt.Errorf("open sandbox output %s: %w", outRel, err)
		}
		if err := writeFile(hostOut, src); err != nil {
			src.Close()
			return exitCode, fmt.Errorf("copy output %s: %w", outRel, err)
		}
		src.Close()
	}

	return exitCode, nil
}

// captureSealedOutputs reads each declared sealed-output file from the
// side-channel directory and routes its contents through the configured
// writer. The file must exist; missing files are an error. Values are
// not logged. The caller is responsible for removing sealedOutDir.
func (e *Executor) captureSealedOutputs(ctx context.Context, a *Action, sealedOutDir string) error {
	for name, ref := range a.SealedOutputs {
		// Reject names that would escape the side-channel directory. Names
		// must be a single path component.
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return fmt.Errorf("action %q: sealed_output name %q is not a single path component", a.ID, name)
		}
		path := filepath.Join(sealedOutDir, name)
		value, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("action %q: sealed_output %q: read %s: %w", a.ID, name, name, err)
		}
		mode := a.SealedOutputModes[name]
		if err := e.SealedOutputWriter(ctx, ref, string(value), mode); err != nil {
			// Zero the buffer before returning so the value is not retained
			// in the heap any longer than necessary.
			for i := range value {
				value[i] = 0
			}
			return fmt.Errorf("action %q: sealed_output %q: store: %w", a.ID, name, err)
		}
		for i := range value {
			value[i] = 0
		}
	}
	return nil
}

// storeOutput hashes a file and stores it in the CAS.
func (e *Executor) storeOutput(ctx context.Context, path string) (cas.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return cas.Digest{}, err
	}
	defer f.Close()
	return e.Store.Put(ctx, f)
}

// restoreOutputs restores cached output blobs to their declared paths.
func (e *Executor) restoreOutputs(ctx context.Context, a *Action, result *cas.ActionResult) error {
	for _, outPath := range a.Outputs {
		dgst, ok := result.Outputs[outPath]
		if !ok {
			return fmt.Errorf("cached result missing output %q", outPath)
		}
		rc, err := e.Store.Get(ctx, dgst)
		if err != nil {
			return err
		}
		if err := writeFile(outPath, rc); err != nil {
			rc.Close()
			return err
		}
		rc.Close()
	}
	return nil
}

// writeFile writes content from rc to the given path, creating parent dirs.
func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func hasRelativeOutputs(outputs []string) bool {
	for _, o := range outputs {
		if !filepath.IsAbs(o) {
			return true
		}
	}
	return false
}

// buildEnv converts an env map to the os/exec []string format.
// A nil map means "inherit parent environment" (backward compat).
// An explicit empty map means "clean environment with no variables".
func buildEnv(env map[string]string) []string {
	if env == nil {
		return nil // nil env = inherit parent (backward compat)
	}
	// Explicit env map (even if empty) = use only declared vars
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
