package dag

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chazu/ewe"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/ewesink"
	"github.com/chazu/mu/internal/pithvm"
	"github.com/chazu/mu/internal/rootexec"
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

// SealedInputResolver resolves one scheduled action's sealed inputs immediately
// before execution. It is never called for cached or dependency-cancelled
// actions, keeping provider reads aligned with actual execution.
type SealedInputResolver func(ctx context.Context, action *Action) (map[string]string, error)

// Executor runs a DAG of actions with parallel scheduling and CAS caching.
type Executor struct {
	NoCache         bool // skip action-result reads; retain CAS for dependencies and outputs
	Store           cas.Store
	Workers         int                          // 0 means runtime.NumCPU()
	ResolvedSecrets map[string]map[string]string // actionID → envName → secret value (never persisted)
	// SealedInputResolver is the production lazy-resolution path. The
	// ResolvedSecrets map remains as a test/library compatibility seam; values
	// returned by this callback take precedence for the scheduled action.
	SealedInputResolver SealedInputResolver
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

	// completed records finished actions (and their output digests) by ID so
	// downstream placeholder inputs can be resolved. Guarded by outputsMu.
	completed map[string]completedAction

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
	e.completed = make(map[string]completedAction)

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

				e.outputsMu.Lock()
				e.completed[status.ID] = completedAction{action: g.Action(status.ID), outputs: status.Outputs}
				e.outputsMu.Unlock()

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
	var workDir *os.File
	var workRoot *os.Root
	if a.ProjectRoot != "" {
		var err error
		workRoot, err = OpenWorkRoot(a.ProjectRoot, a.WorkDir)
		if err != nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q work_dir: %w", a.ID, err)}
		}
		defer workRoot.Close()
		workDir, err = workRoot.Open(".")
		if err != nil {
			return ActionStatus{ID: a.ID, Err: err}
		}
		defer workDir.Close()
	}

	// Upstream-produced inputs carry placeholders until their producers have
	// run; resolve them before the action key is computed.
	if err := e.resolveUpstreamInputs(a); err != nil {
		return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: %w", a.ID, err)}
	}

	// Cache check — only for pure actions.
	if !a.Impure && !e.NoCache {
		key := ComputeActionKey(a)
		if e.Store != nil {
			cached, err := e.Store.GetActionResult(ctx, key)
			if err == nil && cached != nil {
				// Cache hit — restore outputs from CAS.
				if err := e.restoreOutputs(ctx, a, cached, workRoot); err == nil {
					return ActionStatus{ID: a.ID, Cached: true, ExitCode: cached.ExitCode, Outputs: cached.Outputs}
				}
				// On restore failure, fall through to re-execute.
			}
		}
	}

	// Execute the command (or pith Body program).
	if len(a.Command) == 0 && len(a.Body) == 0 && a.EweRef == (cas.Digest{}) {
		return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q has no command, body, or ewe program", a.ID)}
	}

	secrets := e.ResolvedSecrets[a.ID]
	if e.SealedInputResolver != nil && len(a.SealedInputs) > 0 {
		resolved, err := e.SealedInputResolver(ctx, a)
		if err != nil {
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("resolve sealed inputs: %w", err)}
		}
		secrets = resolved
	}
	// Apply per-name delivery modes. "env" (or empty) is the default and
	// exports the value verbatim; "file" writes to a 0600 temp file under a
	// per-action sealed-input directory and exports the path. Sandbox-mode
	// actions cannot use "file" because the temp file lives outside the
	// sandbox view.
	var sealedInDir string
	sealedEnv := make(map[string]string, len(secrets))
	for k, v := range secrets {
		mode := a.SealedInputModes[k]
		switch mode {
		case "", "env":
			sealedEnv[k] = v
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
			sealedEnv[k] = path
		default:
			return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q: sealed_input %q: unknown mode %q", a.ID, k, mode)}
		}
	}

	// muVars holds the per-execution MU_* runtime variables.
	muVars := map[string]string{}

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
		muVars["MU_SEALED_OUT_DIR"] = sealedOutDir
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
		muVars["MU_OUT"] = muOutDir
	}

	// Build the environment once, into a fresh map: a.Env is never mutated
	// (ComputeActionKey runs again after execution, and secrets must never
	// enter the key).
	execEnv := actionEnv(a, os.Environ(), muVars, sealedEnv)

	exitCode, attempts, execErr := e.runWithTimeoutAndRetry(ctx, a, execEnv, workDir, workRoot)

	if execErr != nil {
		return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q failed: %w", a.ID, execErr)}
	}

	// Collect declared outputs into WorkDir so dependents and cache storage can
	// find them. An action may write an output to either location:
	//   - $MU_OUT (the staging dir): ewe/pith actions, or any command that
	//     honors MU_OUT. We copy those into WorkDir.
	//   - its working dir directly: a plain shell command with a relative output
	//     path (e.g. `cowsay ... > greeting.txt`, `go build -o=mu`). exec runs
	//     with cmd.Dir = WorkDir, so the file already lands at the destination.
	// Prefer the staging copy; fall back to an in-place WorkDir output. Error
	// only if the output is in neither — otherwise a legacy bare command that
	// wrote to WorkDir (the pre-$MU_OUT convention) fails to build.
	if muOutDir != "" {
		for _, outRel := range a.Outputs {
			if filepath.IsAbs(outRel) {
				continue
			}
			srcPath := filepath.Join(muOutDir, outRel)
			dstPath := filepath.Join(a.WorkDir, outRel)
			if workRoot != nil {
				dstPath = outRel
			}
			src, err := os.Open(srcPath)
			if err != nil {
				if os.IsNotExist(err) {
					if _, statErr := outputStat(workRoot, dstPath); statErr == nil {
						continue // command wrote the output directly to WorkDir
					}
				}
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Attempts: attempts, Err: fmt.Errorf("action %q: open output %s: %w", a.ID, outRel, err)}
			}
			if err := writeFile(ctx, dstPath, src, workRoot); err != nil {
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
		Version:     cas.ActionResultVersion,
		OutputModes: make(map[string]uint32),
		Outputs:     make(map[string]cas.Digest),
		ExitCode:    exitCode,
	}

	if e.Store != nil && !a.Impure {
		for _, outPath := range a.Outputs {
			absPath := outPath
			if !filepath.IsAbs(outPath) && workRoot == nil {
				absPath = filepath.Join(a.WorkDir, outPath)
			}
			dgst, mode, err := e.storeOutput(ctx, absPath, workRoot)
			if err != nil {
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Err: fmt.Errorf("action %q: storing output %q: %w", a.ID, outPath, err)}
			}
			actionResult.Outputs[outPath] = dgst
			actionResult.OutputModes[outPath] = uint32(mode.Perm())
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
func (e *Executor) runWithTimeoutAndRetry(ctx context.Context, a *Action, env map[string]string, workDir *os.File, workRoot *os.Root) (int, int, error) {
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
			exitCode, err = e.executeInSandbox(attemptCtx, a, env, workRoot)
		} else if a.EweRef != (cas.Digest{}) {
			exitCode, err = e.executeEwe(attemptCtx, a, env, workRoot)
		} else if len(a.Body) > 0 {
			exitCode, err = e.executePithVM(attemptCtx, a, env, workRoot)
		} else {
			exitCode, err = e.executeBare(attemptCtx, a, env, workDir)
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
func (e *Executor) executeBare(ctx context.Context, a *Action, env map[string]string, workDir *os.File) (int, error) {
	if workDir == nil {
		path := a.WorkDir
		if path == "" {
			path = "."
		}
		var err error
		workDir, err = os.Open(path)
		if err != nil {
			return -1, err
		}
		defer workDir.Close()
	}
	cmd, err := rootexec.Command(ctx, a.Command, buildEnv(env), workDir)
	if err != nil {
		return -1, err
	}

	cmd.Stdout = e.subprocessStdout()
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return exitCode, err
}

// executePithVM runs a pith VM program instead of a shell command.
func (e *Executor) executePithVM(ctx context.Context, a *Action, env map[string]string, workRoot *os.Root) (int, error) {
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
	if a.WorkDir != "" {
		cp := make(map[string]string, len(env)+1)
		for k, v := range env {
			cp[k] = v
		}
		cp["MU_WORK_DIR"] = a.WorkDir
		env = cp
	}

	pithvm.RegisterExecDrivers(vm, env, sealedNames, getOutput, e.Store, workRoot)
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

// executeEwe runs an ewe populator program: it restores the program from CAS by
// its digest and runs it through a fresh per-execute ewe registry whose sink
// functions close over this action's reveal (sealed inputs), MU_OUT, and
// WorkDir. Outputs land in $MU_OUT and are copied to WorkDir exactly as for a
// bare action.
func (e *Executor) executeEwe(ctx context.Context, a *Action, env map[string]string, workRoot *os.Root) (int, error) {
	rc, err := e.Store.Get(ctx, a.EweRef)
	if err != nil {
		return 1, fmt.Errorf("ewe: restore program %s: %w", a.EweRef.String(), err)
	}
	src, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return 1, fmt.Errorf("ewe: read program: %w", err)
	}

	// Sealed-input values live in env (env-mode delivery). reveal resolves a
	// secret name to its value; nothing else may reveal them.
	sealedNames := make(map[string]bool, len(a.SealedInputs))
	for name := range a.SealedInputs {
		sealedNames[name] = true
	}
	reveal := func(name string) (string, error) {
		v, ok := env[name]
		if !ok {
			return "", fmt.Errorf("sealed input %q not available to this action", name)
		}
		return v, nil
	}

	// #Env exposes the action environment EXCEPT sealed-input values, which must
	// never enter the CUE layer (reveal happens only in sinks).
	safeEnv := make(map[string]string, len(env))
	for k, v := range env {
		if !sealedNames[k] {
			safeEnv[k] = v
		}
	}

	reg, err := ewesink.NewRegistry(ewesink.Deps{
		WorkRoot: workRoot,
		Reveal:   reveal,
		WorkDir:  a.WorkDir,
		MuOut:    env["MU_OUT"],
		Env:      safeEnv,
	})
	if err != nil {
		return 1, fmt.Errorf("ewe: build registry: %w", err)
	}

	if _, err := ewe.NewProcessor(reg).ProcessSource(ctx, string(src)); err != nil {
		return 1, fmt.Errorf("ewe: %w", err)
	}
	return 0, nil
}

// executeInSandbox runs a command in a hermetic sandbox environment.
// The toolchain artifacts are unpacked into the sandbox, sources are copied in,
// the command runs inside the sandbox, and outputs are copied back to WorkDir.
// The env parameter may include resolved secrets merged with the action's declared env.
func (e *Executor) executeInSandbox(ctx context.Context, a *Action, env map[string]string, workRoot *os.Root) (int, error) {
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
		var err error
		if workRoot != nil {
			err = sb.CopySourcesRoot(workRoot, a.Sources)
		} else {
			err = sb.CopySources(a.WorkDir, a.Sources)
		}
		if err != nil {
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
		if workRoot != nil && !filepath.IsAbs(outRel) {
			hostOut = outRel
		}
		src, err := os.Open(sbOut)
		if err != nil {
			return exitCode, fmt.Errorf("open sandbox output %s: %w", outRel, err)
		}
		if err := writeFile(ctx, hostOut, src, workRoot); err != nil {
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

// storeOutput captures bytes and permission bits from the same open file.
func (e *Executor) storeOutput(ctx context.Context, path string, root *os.Root) (cas.Digest, os.FileMode, error) {
	f, err := outputOpen(root, path)
	if err != nil {
		return cas.Digest{}, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cas.Digest{}, 0, err
	}
	if !info.Mode().IsRegular() {
		return cas.Digest{}, 0, fmt.Errorf("output %q is not a regular file", path)
	}
	digest, err := e.Store.Put(ctx, cas.ContextReader(ctx, f))
	return digest, info.Mode().Perm(), err
}

// restoreOutputs verifies and stages every blob before replacing any output.
// Renames are atomic per file, not a transaction across multiple destinations.
// Legacy receipts cannot recover original modes, so they deliberately miss and
// rebuild instead of guessing that a binary is a non-executable data file.
func (e *Executor) restoreOutputs(ctx context.Context, a *Action, result *cas.ActionResult, root *os.Root) error {
	if len(a.Outputs) > 0 && result.Version != cas.ActionResultVersion {
		return fmt.Errorf("cached result version %d lacks supported output metadata", result.Version)
	}
	type replacement struct {
		staging, destination string
		root                 *os.Root
	}
	files := make([]replacement, 0, len(a.Outputs))
	defer func() {
		for _, file := range files {
			outputRemove(file.root, file.staging)
		}
	}()
	for _, outPath := range a.Outputs {
		dgst, ok := result.Outputs[outPath]
		if !ok {
			return fmt.Errorf("cached result missing output %q", outPath)
		}
		mode, ok := result.OutputModes[outPath]
		if !ok || mode & ^uint32(0o777) != 0 {
			return fmt.Errorf("cached result has invalid permissions for %q", outPath)
		}
		destination := outPath
		if !filepath.IsAbs(destination) && root == nil {
			destination = filepath.Join(a.WorkDir, destination)
		}
		// Store.Get verifies the digest at EOF, which stageFile reads to.
		rc, err := e.Store.Get(ctx, dgst)
		if err != nil {
			return err
		}
		staging, err := stageFile(ctx, destination, rc, os.FileMode(mode), root)
		closeErr := rc.Close()
		if staging != "" {
			files = append(files, replacement{staging, destination, outputRoot(root, destination)})
		}
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, file := range files {
		if err := outputRename(file.root, file.staging, file.destination); err != nil {
			return err
		}
	}
	return nil
}

// writeFile copies an output without losing its producer's permission bits.
func writeFile(ctx context.Context, path string, src *os.File, root *os.Root) error {
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("output %q is not a regular file", path)
	}
	staging, err := stageFile(ctx, path, src, info.Mode().Perm(), root)
	if err != nil {
		return err
	}
	defer outputRemove(outputRoot(root, path), staging)
	if err := ctx.Err(); err != nil {
		return err
	}
	return outputRename(outputRoot(root, path), staging, path)
}

// stageFile creates a private sibling and publishes nothing until reads
// (including a store reader's digest check at EOF), chmod, and close have all
// succeeded.
func stageFile(ctx context.Context, path string, r io.Reader, mode os.FileMode, root *os.Root) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root = outputRoot(root, path)
	var f *os.File
	var name string
	var err error
	if root == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		f, err = os.CreateTemp(filepath.Dir(path), ".mu-output-*")
		if err == nil {
			name = f.Name()
		}
	} else {
		if err = root.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		name = filepath.Join(filepath.Dir(path), ".mu-output-"+rand.Text())
		f, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	}
	if err != nil {
		return "", err
	}

	complete := false
	defer func() {
		if !complete {
			f.Close()
			outputRemove(root, name)
		}
	}()
	if _, err = io.Copy(f, cas.ContextReader(ctx, r)); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	complete = true
	return name, nil
}

// Explicit absolute outputs keep their intentional host-path contract.
// Every relative output is resolved by the pinned directory, never its name.
func outputRoot(root *os.Root, path string) *os.Root {
	if filepath.IsAbs(path) {
		return nil
	}
	return root
}
func outputOpen(root *os.Root, path string) (*os.File, error) {
	if root = outputRoot(root, path); root != nil {
		return root.Open(path)
	}
	return os.Open(path)
}
func outputStat(root *os.Root, path string) (os.FileInfo, error) {
	if root = outputRoot(root, path); root != nil {
		return root.Stat(path)
	}
	return os.Stat(path)
}
func outputRemove(root *os.Root, path string) error {
	if root != nil {
		return root.Remove(path)
	}
	return os.Remove(path)
}
func outputRename(root *os.Root, old, new string) error {
	if root != nil {
		return root.Rename(old, new)
	}
	return os.Rename(old, new)
}

func hasRelativeOutputs(outputs []string) bool {
	for _, o := range outputs {
		if !filepath.IsAbs(o) {
			return true
		}
	}
	return false
}

// buildEnv converts an env map to the os/exec []string format. The result is
// always non-nil, so os/exec never implicitly inherits the parent environment;
// host inheritance is decided once, by actionEnv.
func buildEnv(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
