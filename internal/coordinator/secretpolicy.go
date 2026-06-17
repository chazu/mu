package coordinator

import (
	"fmt"
	"path"

	"github.com/chazu/mu/internal/config"
)

// secretWritePolicy decides whether a given ref may be written to.
// A nil policy means "no allow-list configured" — all writes are
// permitted (back-compat). A non-nil policy with an empty Patterns
// slice means "deny all writes". Otherwise a ref is allowed iff it
// matches at least one pattern under path.Match semantics.
type secretWritePolicy struct {
	Patterns []string
}

// newSecretWritePolicy builds a policy from a project's SecretsConfig.
// Returns nil when no allow-list is configured (writes unrestricted).
// Returns an error when any pattern is syntactically invalid.
func newSecretWritePolicy(cfg *config.SecretsConfig) (*secretWritePolicy, error) {
	if cfg == nil || cfg.WritableRefs == nil {
		return nil, nil
	}
	// Validate each pattern eagerly so we fail at plan time, not on
	// the first sealed_outputs write.
	for _, p := range cfg.WritableRefs {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("secrets.writable_refs: invalid pattern %q: %w", p, err)
		}
	}
	patterns := make([]string, len(cfg.WritableRefs))
	copy(patterns, cfg.WritableRefs)
	return &secretWritePolicy{Patterns: patterns}, nil
}

// Allow reports whether ref may be written to under this policy.
// A nil receiver is permissive (no allow-list). An empty Patterns
// slice denies everything.
func (p *secretWritePolicy) Allow(ref string) bool {
	if p == nil {
		return true
	}
	for _, pat := range p.Patterns {
		ok, err := path.Match(pat, ref)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// Description returns a human-readable summary of the policy state for
// error messages. Returns "" when the policy is nil.
func (p *secretWritePolicy) Description() string {
	if p == nil {
		return ""
	}
	if len(p.Patterns) == 0 {
		return "deny-all (secrets.writable_refs is set but empty)"
	}
	return fmt.Sprintf("%d pattern(s)", len(p.Patterns))
}
