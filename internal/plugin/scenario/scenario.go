// Package scenario defines the YAML/JSON format for mu plugin test
// fixtures and the runner that executes them against a live plugin
// process.
package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is one named test case: a request to send, and how to
// evaluate the response.
type Scenario struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Request     map[string]any `yaml:"request" json:"request"`
	Expect      Expect         `yaml:"expect" json:"expect"`

	// SkipIfMissingCapability, if non-empty, causes the scenario to be
	// marked skipped rather than failed when the plugin's discover
	// response does not include the named capability.
	SkipIfMissingCapability string `yaml:"skip_if_missing_capability,omitempty" json:"skip_if_missing_capability,omitempty"`

	// Path is populated by loaders so Golden can resolve relative paths.
	Path string `yaml:"-" json:"-"`
}

// Expect selects one of three assertion modes. Exactly one of Exact /
// Shape / Golden should be set.
type Expect struct {
	Match  string         `yaml:"match" json:"match"` // "exact" | "shape" | "golden"
	Exact  map[string]any `yaml:"exact,omitempty" json:"exact,omitempty"`
	Shape  map[string]any `yaml:"shape,omitempty" json:"shape,omitempty"`
	Golden string         `yaml:"golden,omitempty" json:"golden,omitempty"` // relative to scenario file
	Within time.Duration  `yaml:"within,omitempty" json:"within,omitempty"`
}

// Load reads a scenario file (YAML or JSON, auto-detected by extension).
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Scenario{Path: path}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if err := json.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if s.Expect.Match == "" {
		s.Expect.Match = "shape"
	}
	return s, nil
}

// LoadDir loads every *.yaml / *.yml / *.json file under dir as a
// scenario. Returns scenarios sorted by name.
func LoadDir(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Scenario
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		if strings.HasSuffix(e.Name(), ".golden.json") {
			continue // golden files are companions, not scenarios
		}
		s, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
