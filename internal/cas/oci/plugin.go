package oci

const (
	// MediaTypePluginConfig is the config blob media type for a mu plugin artifact.
	MediaTypePluginConfig = "application/vnd.mu.plugin.v1+json"

	// MediaTypePluginFile is the layer media type for a single plugin file.
	MediaTypePluginFile = "application/vnd.mu.plugin.file.v1"

	// MediaTypePluginIndexConfig is the config blob media type for the plugin index.
	MediaTypePluginIndexConfig = "application/vnd.mu.plugin-index.v1+json"

	// ArtifactTypePlugin is the OCI 1.1 artifactType for a plugin artifact.
	ArtifactTypePlugin = "application/vnd.mu.plugin.v1"

	// ArtifactTypePluginIndex is the OCI 1.1 artifactType for the plugin index.
	ArtifactTypePluginIndex = "application/vnd.mu.plugin-index.v1"

	// PluginRepoPrefix is the path segment under which mu plugins live within
	// the configured cache push repository.
	PluginRepoPrefix = "mu/plugins"

	// PluginIndexRef is the path under cache.push.repository where the plugin
	// index is tagged. Full ref: "<registry>/<repository>/mu/plugin-index:v1".
	PluginIndexRef = "mu/plugin-index"

	// PluginIndexTag is the tag used for the plugin index artifact.
	PluginIndexTag = "v1"
)

// PluginConfig is the JSON payload of a plugin artifact's config blob.
// It mirrors config.PluginManifest but is self-contained so the artifact
// is interpretable without the source project.
type PluginConfig struct {
	Name       string   `json:"name"`
	Entrypoint string   `json:"entrypoint"`
	Toolchain  string   `json:"toolchain,omitempty"`
	Files      []string `json:"files,omitempty"`
	Guide      string   `json:"guide,omitempty"`
	Digest     string   `json:"digest,omitempty"`
	Source     string   `json:"source,omitempty"`
}

// PluginIndex is the JSON payload of the plugin-index artifact's config blob.
// It lists plugin names known in this registry's mu namespace so clients can
// enumerate without relying on the OCI _catalog endpoint.
type PluginIndex struct {
	SchemaVersion int      `json:"schemaVersion"`
	Plugins       []string `json:"plugins"`
}

// PluginTag returns the OCI tag used for a plugin artifact given its sha256
// hex digest. We use the first 12 hex chars so tags are short, stable, and
// don't collide with future semver-tagging schemes.
func PluginTag(sha256Hex string) string {
	if len(sha256Hex) > 12 {
		sha256Hex = sha256Hex[:12]
	}
	return "sha256-" + sha256Hex
}
