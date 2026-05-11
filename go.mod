module github.com/chau/mu

go 1.25.8

require (
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	oras.land/oras-go/v2 v2.6.0
)

require github.com/chazu/pith v0.0.0-00010101000000-000000000000

require golang.org/x/sync v0.19.0

require (
	cuelang.org/go v0.16.0
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	golang.org/x/term v0.42.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cuelabs.dev/go/oci/ociregistry v0.0.0-20251212221603-3adeb8663819 // indirect
	github.com/cockroachdb/apd/v3 v3.2.1 // indirect
	github.com/emicklei/proto v1.14.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/protocolbuffers/txtpbfmt v0.0.0-20260217160748-a481f6a22f94 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
)

replace github.com/chazu/pith => ../pith
