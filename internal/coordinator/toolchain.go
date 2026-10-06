package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/chazu/mu/internal/cas"
)

// ToolchainManifest records the artifacts produced by building from scratch a toolchain.
type ToolchainManifest struct {
	Name      string            `json:"name"`      // e.g. "go"
	Version   string            `json:"version"`   // e.g. "1.25.7"
	Artifacts map[string]string `json:"artifacts"` // logical name -> CAS digest string (e.g. "bin/go" -> "sha256:abc...")
}

// ToolchainRegistry stores and retrieves toolchain manifests, backed by CAS for persistence.
type ToolchainRegistry struct {
	store     cas.Store
	manifests map[string]*ToolchainManifest // name -> manifest (in-memory cache)
	mu        sync.RWMutex
}

// NewToolchainRegistry creates a new ToolchainRegistry backed by the given CAS store.
func NewToolchainRegistry(store cas.Store) *ToolchainRegistry {
	return &ToolchainRegistry{
		store:     store,
		manifests: make(map[string]*ToolchainManifest),
	}
}

// actionKey computes a deterministic ActionKey from the toolchain name and version.
// The key material is SHA256("toolchain:<name>:<version>").
func actionKey(name, version string) cas.ActionKey {
	h := sha256.Sum256([]byte("toolchain:" + name + ":" + version))
	return cas.ActionKey{
		Digest: cas.NewSHA256(hex.EncodeToString(h[:])),
	}
}

// Register stores the manifest in CAS as a JSON blob and caches it in-memory.
//
// It computes a deterministic key from name+version, stores the manifest JSON
// as a blob, then stores an ActionResult mapping "manifest" to the blob's digest.
func (r *ToolchainRegistry) Register(ctx context.Context, manifest *ToolchainManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("toolchain: marshal manifest: %w", err)
	}

	// Store the manifest JSON as a CAS blob.
	blobDigest, err := r.store.Put(ctx, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("toolchain: put manifest blob: %w", err)
	}

	// Store an ActionResult mapping "manifest" -> blob digest.
	key := actionKey(manifest.Name, manifest.Version)
	result := &cas.ActionResult{
		Outputs: map[string]cas.Digest{
			"manifest": blobDigest,
		},
	}
	if err := r.store.PutActionResult(ctx, key, result); err != nil {
		return fmt.Errorf("toolchain: put action result: %w", err)
	}

	// Cache in-memory.
	r.mu.Lock()
	r.manifests[manifest.Name] = manifest
	r.mu.Unlock()

	return nil
}

// Lookup retrieves a manifest by name and version. It checks the in-memory cache
// first, then falls back to CAS. Returns (nil, nil) if the toolchain is not found.
func (r *ToolchainRegistry) Lookup(ctx context.Context, name, version string) (*ToolchainManifest, error) {
	// Check in-memory cache.
	r.mu.RLock()
	if m, ok := r.manifests[name]; ok && m.Version == version {
		r.mu.RUnlock()
		return m, nil
	}
	r.mu.RUnlock()

	// Look up in CAS.
	key := actionKey(name, version)
	result, err := r.store.GetActionResult(ctx, key)
	if errors.Is(err, cas.ErrUnavailable) && ctx.Err() == nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("toolchain: get action result: %w", err)
	}
	if result == nil {
		return nil, nil
	}

	blobDigest, ok := result.Outputs["manifest"]
	if !ok {
		return nil, fmt.Errorf("toolchain: action result missing 'manifest' output")
	}

	rc, err := r.store.Get(ctx, blobDigest)
	if err != nil {
		return nil, fmt.Errorf("toolchain: get manifest blob: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("toolchain: read manifest blob: %w", err)
	}

	var manifest ToolchainManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("toolchain: unmarshal manifest: %w", err)
	}

	// Cache in-memory.
	r.mu.Lock()
	r.manifests[manifest.Name] = &manifest
	r.mu.Unlock()

	return &manifest, nil
}

// Get returns the cached manifest for the given toolchain name (any version).
// It only checks the in-memory cache and returns nil if not found.
func (r *ToolchainRegistry) Get(name string) *ToolchainManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.manifests[name]
}

// ExtractBinary extracts a single artifact from CAS to a file on disk and returns
// the path. The file is written to baseDir/<name>/<artifact> with executable
// permissions. This is used to get a real filesystem path for tools that need to
// be invoked directly (e.g. bb for running plugin scripts).
func (r *ToolchainRegistry) ExtractBinary(ctx context.Context, name, artifact, baseDir string) (string, error) {
	r.mu.RLock()
	m, ok := r.manifests[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("toolchain %q not registered", name)
	}

	digestStr, ok := m.Artifacts[artifact]
	if !ok {
		return "", fmt.Errorf("toolchain %q has no artifact %q", name, artifact)
	}

	dgst, err := cas.ParseDigest(digestStr)
	if err != nil {
		return "", fmt.Errorf("parse digest for %s/%s: %w", name, artifact, err)
	}

	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid toolchain cache name %q", name)
	}
	if dgst.Algorithm != "sha256" || len(dgst.Hash) != 64 {
		return "", fmt.Errorf("toolchain binary requires a full SHA-256 identity")
	}
	if _, err := hex.DecodeString(dgst.Hash); err != nil {
		return "", err
	}
	destDir := filepath.Join(baseDir, name, dgst.Hash)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, filepath.Base(artifact))
	matches := func(path string) bool {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		defer f.Close()
		actual, err := cas.ComputeDigest(cas.ContextReader(ctx, f))
		return err == nil && actual == dgst
	}
	if matches(destPath) {
		return destPath, nil
	}
	// A legacy name-only binary may be adopted only after digest verification.
	// Snapshot it at a full-digest path so another version cannot replace it.
	var source io.ReadCloser
	legacy := filepath.Join(baseDir, name, filepath.Base(artifact))
	if matches(legacy) {
		source, err = os.Open(legacy)
	} else {
		source, err = r.store.Get(ctx, dgst)
	}
	if err != nil {
		return "", fmt.Errorf("required toolchain binary %s unavailable locally: %w", dgst, err)
	}
	tmp, err := os.CreateTemp(destDir, ".tool-*")
	if err != nil {
		source.Close()
		return "", err
	}
	defer os.Remove(tmp.Name())
	actual, copyErr := cas.ComputeDigest(io.TeeReader(cas.ContextReader(ctx, source), tmp))
	readCloseErr := source.Close()
	if copyErr == nil {
		copyErr = readCloseErr
	}
	if copyErr == nil && actual != dgst {
		copyErr = fmt.Errorf("toolchain bytes do not match %s", dgst)
	}
	if copyErr == nil {
		copyErr = tmp.Chmod(0o755)
	}
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(tmp.Name(), destPath); err != nil {
		return "", err
	}
	return destPath, nil
}

// ArtifactsMap returns the artifacts map for the named toolchain, or nil if
// the toolchain is not registered in the in-memory cache. This is intended
// to be passed as toolchain_artifacts in plugin plan requests.
func (r *ToolchainRegistry) ArtifactsMap(name string) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[name]
	if !ok {
		return nil
	}
	cp := make(map[string]string, len(m.Artifacts))
	for k, v := range m.Artifacts {
		cp[k] = v
	}
	return cp
}
