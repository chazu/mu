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

	// Validate service dependencies reference existing services.
	for i, s := range cfg.Services {
		if s.Name == "" {
			continue
		}
		label := fmt.Sprintf("services[%d]", i)
		for dep := range s.DependsOn {
			if !seenServices[dep] {
				errs = append(errs, fmt.Sprintf("%s: depends_on references unknown service %q", label, dep))
			}
		}
	}

	// Validate toolchains.
	seenToolchains := make(map[string]bool)
	for i, tc := range cfg.Toolchains {
		label := fmt.Sprintf("toolchains[%d]", i)

		if tc.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"toolchain\"", label))
		} else {
			if seenToolchains[tc.Name] {
				errs = append(errs, fmt.Sprintf("%s: duplicate toolchain name %q", label, tc.Name))
			}
			seenToolchains[tc.Name] = true
		}

		if tc.Plugin == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"plugin\"", label))
		}
	}

	// Validate plugins.
	seenPlugins := make(map[string]bool)
	for i, p := range cfg.Plugins {
		label := fmt.Sprintf("plugins[%d]", i)

		if p.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"name\"", label))
		} else {
			if seenPlugins[p.Name] {
				errs = append(errs, fmt.Sprintf("%s: duplicate plugin name %q", label, p.Name))
			}
			seenPlugins[p.Name] = true
		}

		if len(p.Command) == 0 {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"command\"", label))
		}
	}

	// Validate triggers.
	seenTriggers := make(map[string]bool)
	for i, tr := range cfg.Triggers {
		label := fmt.Sprintf("triggers[%d]", i)

		if tr.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"trigger\"", label))
		} else {
			if seenTriggers[tr.Name] {
				errs = append(errs, fmt.Sprintf("%s: duplicate trigger name %q", label, tr.Name))
			}
			seenTriggers[tr.Name] = true
		}

		if len(tr.Watch) == 0 {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"watch\"", label))
		}
	}

	// Validate cache config.
	if cfg.Cache != nil {
		for i, b := range cfg.Cache.Backends {
			label := fmt.Sprintf("cache.backends[%d]", i)

			if b.Type != "disk" && b.Type != "oci" {
				errs = append(errs, fmt.Sprintf("%s: type must be \"disk\" or \"oci\", got %q", label, b.Type))
			}
			if b.Type == "disk" && b.Path == "" {
				errs = append(errs, fmt.Sprintf("%s: disk backend requires \"path\"", label))
			}
			if b.Type == "oci" && b.Registry == "" {
				errs = append(errs, fmt.Sprintf("%s: oci backend requires \"registry\"", label))
			}
		}
	}

	// Validate preprocessor.
	if cfg.Preprocessor != nil {
		if cfg.Preprocessor.Extension == "" {
			errs = append(errs, "preprocessor: missing required field \"extension\"")
		}
		if len(cfg.Preprocessor.Command) == 0 {
			errs = append(errs, "preprocessor: missing required field \"command\"")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
