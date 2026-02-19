package config

import (
	"fmt"
	"strings"
)

// Validate checks the loaded ProjectConfig for structural errors. It
// returns a combined error describing all issues found, or nil if the
// config is valid.
func Validate(cfg *ProjectConfig) error {
	var errs []string

	seenTargets := make(map[string]bool)
	for i, t := range cfg.Targets {
		label := fmt.Sprintf("targets[%d]", i)

		if t.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"target\"", label))
		} else {
			if !strings.HasPrefix(t.Name, "//") {
				errs = append(errs, fmt.Sprintf("%s: target name %q must start with \"//\"", label, t.Name))
			}
			if seenTargets[t.Name] {
				errs = append(errs, fmt.Sprintf("%s: duplicate target name %q", label, t.Name))
			}
			seenTargets[t.Name] = true
		}

		if t.Toolchain == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"toolchain\"", label))
		}
	}

	seenServices := make(map[string]bool)
	for i, s := range cfg.Services {
		label := fmt.Sprintf("services[%d]", i)

		if s.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"service\"", label))
		} else {
			if seenServices[s.Name] {
				errs = append(errs, fmt.Sprintf("%s: duplicate service name %q", label, s.Name))
			}
			seenServices[s.Name] = true
		}

		if s.Runtime != "docker" && s.Runtime != "host" {
			errs = append(errs, fmt.Sprintf("%s: runtime must be \"docker\" or \"host\", got %q", label, s.Runtime))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
