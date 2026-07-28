// Package plugincatalog implements the source-package catalog used by
// `mu plugin search`, `install`, and `update`.
package plugincatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/chazu/mu/internal/builtin"
)

const (
	// DefaultURL points at the catalog asset published by the official
	// mu-plugins GitHub releases. Release assets are immutable; this URL only
	// selects the current GitHub release.
	DefaultURL = "https://github.com/chazu/mu-plugins/releases/latest/download/catalog.json"

	CatalogSchemaVersion = 1
)

// Catalog is the generated release catalog published by mu-plugins.
type Catalog struct {
	SchemaVersion int      `json:"schema_version"`
	Repository    string   `json:"repository"`
	ReleaseTag    string   `json:"release_tag"`
	Plugins       []Plugin `json:"plugins"`
}

// Plugin describes one immutable source package in a Catalog.
type Plugin struct {
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	Description  string        `json:"description,omitempty"`
	AssetURL     string        `json:"asset_url"`
	SHA256       string        `json:"sha256"`
	Path         string        `json:"path"`
	Entrypoint   string        `json:"entrypoint"`
	Toolchain    string        `json:"toolchain,omitempty"`
	Requirements []string      `json:"requirements,omitempty"`
	Schemas      []Schema      `json:"schemas,omitempty"`
	PUDLMappings []PUDLMapping `json:"pudl_mappings,omitempty"`
	Build        *BuildSpec    `json:"build,omitempty"`
}

// Schema identifies a plugin-owned wire schema bundled with a package.
type Schema struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

// PUDLMapping identifies the semantic schema a PUDL integration should use
// for one resource type. mu preserves this metadata in the lockfile but does
// not interpret it.
type PUDLMapping struct {
	ResourceType string `json:"resource_type"`
	Schema       string `json:"schema"`
}

// BuildSpec describes a source build needed before the package can be
// bundled. It is currently used by the Go source-only envsecret plugin.
type BuildSpec struct {
	Command []string `json:"command"`
	Sources []string `json:"sources,omitempty"`
}

// Load decodes and validates a catalog from r.
func Load(r io.Reader) (*Catalog, error) {
	var catalog Catalog
	dec := json.NewDecoder(r)
	if err := dec.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("catalog contains trailing JSON")
		}
		return nil, fmt.Errorf("read catalog tail: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

// Fetch retrieves and validates a catalog. file:// URLs are accepted for
// local development and tests; release assets themselves remain HTTP(S).
func Fetch(ctx context.Context, rawURL string) (*Catalog, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse catalog URL %q: %w", rawURL, err)
	}
	if u.Scheme == "file" {
		f, err := os.Open(u.Path)
		if err != nil {
			return nil, fmt.Errorf("open catalog %s: %w", rawURL, err)
		}
		defer f.Close()
		return Load(f)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("catalog URL %q: scheme must be http, https, or file", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create catalog request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch catalog: HTTP status %d", resp.StatusCode)
	}
	return Load(resp.Body)
}

// Validate checks the catalog invariants required by the installer.
func (c *Catalog) Validate() error {
	if c.SchemaVersion != CatalogSchemaVersion {
		return fmt.Errorf("catalog schema_version %d is unsupported (want %d)", c.SchemaVersion, CatalogSchemaVersion)
	}
	if strings.TrimSpace(c.Repository) == "" {
		return fmt.Errorf("catalog repository is required")
	}
	if strings.TrimSpace(c.ReleaseTag) == "" {
		return fmt.Errorf("catalog release_tag is required")
	}
	if len(c.Plugins) == 0 {
		return fmt.Errorf("catalog has no plugins")
	}
	seen := make(map[string]struct{}, len(c.Plugins))
	for i := range c.Plugins {
		p := &c.Plugins[i]
		if p.Name == "" {
			return fmt.Errorf("catalog plugin %d: name is required", i)
		}
		key := p.Name + "@" + p.Version
		if _, ok := seen[key]; ok {
			return fmt.Errorf("catalog contains duplicate plugin %q version %q", p.Name, p.Version)
		}
		seen[key] = struct{}{}
		if _, err := parseVersion(p.Version); err != nil {
			return fmt.Errorf("catalog plugin %q: %w", p.Name, err)
		}
		if err := validateHTTPURL(p.AssetURL, "asset_url"); err != nil {
			return fmt.Errorf("catalog plugin %q: %w", p.Name, err)
		}
		if !isSHA256(p.SHA256) {
			return fmt.Errorf("catalog plugin %q: sha256 must be 64 lowercase hex characters", p.Name)
		}
		if err := validateRelativePath(p.Path, "path"); err != nil {
			return fmt.Errorf("catalog plugin %q: %w", p.Name, err)
		}
		if err := validateRelativePath(p.Entrypoint, "entrypoint"); err != nil {
			return fmt.Errorf("catalog plugin %q: %w", p.Name, err)
		}
		for j, schema := range p.Schemas {
			if schema.Module == "" || schema.Version == "" {
				return fmt.Errorf("catalog plugin %q schema %d: module and version are required", p.Name, j)
			}
			if err := validateRelativePath(schema.Path, "path"); err != nil {
				return fmt.Errorf("catalog plugin %q schema %d: %w", p.Name, j, err)
			}
		}
		if p.Build != nil {
			if len(p.Build.Command) == 0 || p.Build.Command[0] == "" {
				return fmt.Errorf("catalog plugin %q: build command is required", p.Name)
			}
			for j, source := range p.Build.Sources {
				if err := validateRelativePath(source, "build source"); err != nil {
					return fmt.Errorf("catalog plugin %q build source %d: %w", p.Name, j, err)
				}
			}
		}
	}
	return nil
}

// Search returns catalog entries matching query by name, description, or
// requirement. Empty query returns all entries. Results are deterministic.
func (c *Catalog) Search(query string) []Plugin {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]Plugin, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		if query == "" || strings.Contains(strings.ToLower(p.Name), query) ||
			strings.Contains(strings.ToLower(p.Description), query) ||
			containsFold(p.Requirements, query) {
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return compareVersions(result[i].Version, result[j].Version) > 0
	})
	return result
}

// Select resolves an exact name and optional version. With no version it
// selects the highest semantic version in the catalog.
func (c *Catalog) Select(name, version string) (Plugin, error) {
	var matches []Plugin
	for _, p := range c.Plugins {
		if p.Name == name && (version == "" || sameVersion(p.Version, version)) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		if version == "" {
			return Plugin{}, fmt.Errorf("plugin %q is not present in catalog", name)
		}
		return Plugin{}, fmt.Errorf("plugin %q version %q is not present in catalog", name, version)
	}
	sort.Slice(matches, func(i, j int) bool { return compareVersions(matches[i].Version, matches[j].Version) > 0 })
	return matches[0], nil
}

// DownloadAsset fetches one catalog asset and verifies its SHA-256 hash.
func DownloadAsset(ctx context.Context, p Plugin, destPath string) error {
	if err := builtin.ForgeFetch(ctx, p.AssetURL, p.SHA256, destPath); err != nil {
		return fmt.Errorf("download plugin %q: %w", p.Name, err)
	}
	return nil
}

// AssetDigest computes a SHA-256 digest for a local asset. It is useful for
// tests and for callers that want an independent post-download assertion.
func AssetDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func containsFold(values []string, query string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func validateHTTPURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an absolute http(s) URL", field)
	}
	return nil
}

func validateRelativePath(value, field string) error {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be a non-empty relative path", field)
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("%s %q must use a relative slash-separated path", field, value)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%s %q escapes its package", field, value)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

type parsedVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func parseVersion(value string) (parsedVersion, error) {
	value = strings.TrimPrefix(value, "v")
	base, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return parsedVersion{}, fmt.Errorf("version %q is not semantic version major.minor.patch", value)
	}
	result := parsedVersion{prerelease: prerelease}
	var err error
	if result.major, err = strconv.Atoi(parts[0]); err != nil || result.major < 0 {
		return parsedVersion{}, fmt.Errorf("version %q has invalid major", value)
	}
	if result.minor, err = strconv.Atoi(parts[1]); err != nil || result.minor < 0 {
		return parsedVersion{}, fmt.Errorf("version %q has invalid minor", value)
	}
	if result.patch, err = strconv.Atoi(parts[2]); err != nil || result.patch < 0 {
		return parsedVersion{}, fmt.Errorf("version %q has invalid patch", value)
	}
	return result, nil
}

func sameVersion(left, right string) bool {
	l, errL := parseVersion(left)
	r, errR := parseVersion(right)
	return errL == nil && errR == nil && l == r
}

func compareVersions(left, right string) int {
	l, errL := parseVersion(left)
	r, errR := parseVersion(right)
	if errL != nil || errR != nil {
		return strings.Compare(left, right)
	}
	for _, pair := range [][2]int{{l.major, r.major}, {l.minor, r.minor}, {l.patch, r.patch}} {
		if pair[0] != pair[1] {
			if pair[0] > pair[1] {
				return 1
			}
			return -1
		}
	}
	if l.prerelease == r.prerelease {
		return 0
	}
	if l.prerelease == "" {
		return 1
	}
	if r.prerelease == "" {
		return -1
	}
	return strings.Compare(l.prerelease, r.prerelease)
}
