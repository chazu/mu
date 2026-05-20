//go:build !linux && !darwin

package sandbox

import "context"

func detectIsolation() IsolationLevel { return IsolationCopy }

func (s *Sandbox) execIsolated(_ context.Context, _ []string, _ map[string]string, _ bool) (int, error, bool) {
	return 0, nil, false
}

// IsSandboxInit always returns false on unsupported platforms.
func IsSandboxInit() bool { return false }

// RunInit is a no-op on unsupported platforms.
func RunInit() {}
