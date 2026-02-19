package dag

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/chau/mu/internal/cas"
)

// ActionStatus represents the result of executing a single action.
type ActionStatus struct {
	ID       string
	Cached   bool
	ExitCode int
	Err      error
}

// ExecuteResult holds the outcome of a full DAG execution.
type ExecuteResult struct {
	Completed []ActionStatus
	Failed    []ActionStatus
	Cancelled []string // action IDs cancelled due to upstream failure
}

// Executor runs a DAG of actions with parallel scheduling and CAS caching.
type Executor struct {
	Store   cas.Store
	Workers int // 0 means runtime.NumCPU()
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
func (e *Executor) executeAction(ctx context.Context, a *Action) ActionStatus {
	key := ComputeActionKey(a)

	// Check cache.
	if e.Store != nil {
		cached, err := e.Store.GetActionResult(ctx, key)
		if err == nil && cached != nil {
			// Cache hit — restore outputs from CAS.
			if err := e.restoreOutputs(ctx, a, cached); err == nil {
				return ActionStatus{ID: a.ID, Cached: true, ExitCode: cached.ExitCode}
			}
			// On restore failure, fall through to re-execute.
		}
	}

	// Execute the command.
	if len(a.Command) == 0 {
		return ActionStatus{ID: a.ID, Err: fmt.Errorf("action %q has no command", a.ID)}
	}

	cmd := exec.CommandContext(ctx, a.Command[0], a.Command[1:]...)
	cmd.Dir = a.WorkDir
	cmd.Env = buildEnv(a.Env)
	cmd.Stdout = os.Stdout // TODO: capture per-action in later phases
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := cmd.ProcessState.ExitCode()
	if err != nil {
		return ActionStatus{ID: a.ID, ExitCode: exitCode, Err: fmt.Errorf("action %q failed: %w", a.ID, err)}
	}

	// Hash declared outputs and store in CAS.
	actionResult := &cas.ActionResult{
		Outputs:  make(map[string]cas.Digest),
		ExitCode: exitCode,
	}

	if e.Store != nil {
		for _, outPath := range a.Outputs {
			dgst, err := e.storeOutput(ctx, outPath)
			if err != nil {
				return ActionStatus{ID: a.ID, ExitCode: exitCode, Err: fmt.Errorf("action %q: storing output %q: %w", a.ID, outPath, err)}
			}
			actionResult.Outputs[outPath] = dgst
		}

		if err := e.Store.PutActionResult(ctx, key, actionResult); err != nil {
			// Cache write failure is not fatal — log but don't fail the action.
			// In v1 we just ignore this.
		}
	}

	return ActionStatus{ID: a.ID, ExitCode: exitCode}
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
	if dir := dirOf(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// dirOf returns the directory portion of a path, or empty string for a bare filename.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// buildEnv converts an env map to the os/exec []string format.
// Returns nil (inherit nothing) if the map is empty — actions get minimal env.
func buildEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

