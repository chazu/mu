package config

import (
	"fmt"
	"strings"

	"github.com/chazu/mu/internal/cas"
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
		if p.SHA256 != "" && !cas.IsSHA256Hex(p.SHA256) {
			errs = append(errs, fmt.Sprintf("%s: \"sha256\" must be %d lowercase hex characters", label, cas.SHA256HexLen))
		}
		if hasDigest {
			if _, err := cas.ParseFullDigest(p.Digest); err != nil {
				errs = append(errs, fmt.Sprintf("%s: \"digest\" must be a full \"sha256:<64 hex>\" identity: %v", label, err))
			}
		}
		// Runtime field removed — toolchain is declared in plugin manifest.
	}

	// Validate cache config.
	if cfg.Cache != nil && cfg.Cache.Remote != "" {
		r := cfg.Cache.Remote
		if strings.Contains(r, "://") || !strings.Contains(r, "/") {
			errs = append(errs, fmt.Sprintf("cache.remote: want a repository reference \"host/namespace/repo\" without a scheme, got %q", r))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
