package dag

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/sandbox"
)

// ActionStatus represents the result of executing a single action.
type ActionStatus struct {
	ID       string
	Cached   bool
	ExitCode int
	Err      error
	Outputs  map[string]cas.Digest // output name -> content digest
}

// ExecuteResult holds the outcome of a full DAG execution.
type ExecuteResult struct {
	Completed []ActionStatus
	Failed    []ActionStatus
	Cancelled []string // action IDs cancelled due to upstream failure
}

// Executor runs a DAG of actions with parallel scheduling and CAS caching.
type Executor struct {
	Store           cas.Store
	Workers         int                          // 0 means runtime.NumCPU()
	ResolvedSecrets map[string]map[string]string // actionID → envName → secret value (never persisted)
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

	// Execute the command.
	if len(a.Command) == 0 {
		return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q has no command", a.ID)}
	}

	// Merge resolved secrets into a copy of the env. We must not mutate a.Env
	// because ComputeActionKey is called again after execution for cache storage,
	// and secrets must never be part of the cache key.
	execEnv := a.Env
	if secrets := e.ResolvedSecrets[a.ID]; len(secrets) > 0 {
		execEnv = make(map[string]string, len(a.Env)+len(secrets))
		for k, v := range a.Env {
			execEnv[k] = v
		}
		for k, v := range secrets {
			execEnv[k] = v
		}
	}

	var exitCode int
	var execErr error

	if a.Toolchain != nil {
		exitCode, execErr = e.executeInSandbox(ctx, a, execEnv)
	} else {
		exitCode, execErr = e.executeBare(ctx, a, execEnv)
	}

	if execErr != nil {
		return ActionStatus{ID: a.ID, ExitCode: exitCode, Err: fmt.Errorf("action %q failed: %w", a.ID, execErr)}
	}

	// Hash declared outputs and store in CAS — only for pure actions.
	actionResult := &cas.ActionResult{
		Outputs:  make(map[string]cas.Digest),
		ExitCode: exitCode,
	}

	if e.Store != nil && !a.Impure {
		for _, outPath := range a.Outputs {
			dgst, err := e.storeOutput(ctx, outPath)
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

	return ActionStatus{ID: a.ID, ExitCode: exitCode, Outputs: actionResult.Outputs}
}

// executeBare runs a command directly on the host (no sandbox).
// Used for actions without a toolchain, preserving backward compatibility.
// The env parameter may include resolved secrets merged with the action's declared env.
func (e *Executor) executeBare(ctx context.Context, a *Action, env map[string]string) (int, error) {
	cmd := exec.CommandContext(ctx, a.Command[0], a.Command[1:]...)
	cmd.Dir = a.WorkDir
	cmd.Env = buildEnv(env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return exitCode, err
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

