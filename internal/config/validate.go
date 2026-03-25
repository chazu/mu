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

		if tc.From == "" {
			errs = append(errs, fmt.Sprintf("%s: missing required field \"from\"", label))
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

		hasCommand := len(p.Command) > 0
		hasScript := p.Script != ""
		hasURL := p.URL != ""
		hasDigest := p.Digest != ""
		sources := 0
		if hasCommand {
			sources++
		}
		if hasScript {
			sources++
		}
		if hasURL {
			sources++
		}
		if hasDigest {
			sources++
		}
		if sources == 0 {
			errs = append(errs, fmt.Sprintf("%s: must set one of \"command\", \"script\", \"url\", or \"digest\"", label))
		}
		if sources > 1 {
			errs = append(errs, fmt.Sprintf("%s: only one of \"command\", \"script\", \"url\", or \"digest\" may be set", label))
		}
		if hasURL && p.SHA256 == "" {
			errs = append(errs, fmt.Sprintf("%s: \"url\" requires \"sha256\"", label))
		}
		if p.Runtime != "" && p.Runtime != "auto" && p.Runtime != "bb" && p.Runtime != "none" {
			errs = append(errs, fmt.Sprintf("%s: invalid runtime %q (must be \"auto\", \"bb\", or \"none\")", label, p.Runtime))
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
