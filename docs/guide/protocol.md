mu guide protocol — the NDJSON plugin protocol

Plugins communicate with mu over stdin/stdout using NDJSON (newline-
delimited JSON). Each line is a complete JSON object.

  The Go representation of every message below lives in
  sdk/muplugin/types.go — that package is the canonical Go binding
  for this protocol. Plugin authors writing in Go should use
  sdk/muplugin (see 'mu guide sdk'); the wire format documented
  here is the contract for any other language.

LIFECYCLE

  1. mu starts the plugin process.
  2. mu sends {"method": "discover"} — plugin replies with metadata.
  3. mu sends plan/observe/resolve_secret requests as needed.
  4. mu closes stdin when done — plugin exits.

METHODS

  discover (required)
    Request:  {"method": "discover"}
    Response: {
      "name": "myplugin",
      "version": "0.1.0",
      "protocol_version": 1,
      "consumes": ["source:any"],
      "produces": ["binary"],
      "capabilities": ["discover", "plan", "observe"],
      "config_schema": {"output": {"type": "string"}}
    }

  plan (required)
    Request:  {
      "method": "plan",
      "target": {"name": "//app", "toolchain": "go", "sources": [...], "config": {...}},
      "deps": [{"target": "//lib", "artifacts": {"binary": "lib/libfoo.a"}}],
      "toolchain_artifacts": {"go": "/path/to/go"}
    }
    Response: {
      "actions": [
        {
          "id": "compile",
          "command": ["go", "build", "-o", "app", "./cmd/app"],
          "inputs": {"main.go": "main.go"},
          "outputs": ["app"],
          "depends_on": [],
          "env": {"GOOS": "linux"},
          "network": false
        }
      ],
      "declared_outputs": {"binary": "app"}
    }

  observe (optional)
    Request:  {
      "method": "observe",
      "target": {"name": "//infra/vpc", "toolchain": "aws", "config": {...}},
      "secrets": {"AWS_SECRET_KEY": "actual-value"}
    }
    Response: {
      "current": {
        "records": [
          {"_schema": "aws.ec2.vpc", "vpc_id": "vpc-123", "cidr_block": "10.0.0.0/16"}
        ]
      }
    }

  resolve_secret (optional)
    Request:  {"method": "resolve_secret", "secret_ref": "deploy/token"}
    Response: {"value": "the-secret-value"}

  store_secret (optional, paired with resolve_secret)
    Request:  {"method": "store_secret",
               "secret_ref": "deploy/token",
               "secret_value": "...",
               "secret_mode": "create" | "overwrite" | "create_if_absent"}
    Response: {} on success, {"error": "..."} on failure.
    The plugin must implement all three modes; create_if_absent
    is a no-op if the ref already exists. Capability:
    "store_secret" must be present in discover.

TARGET INFO FIELDS

  Target info is sent in plan and observe requests. Fields beyond
  the obvious name/toolchain/sources/config:

  sealed_inputs       map[NAME]ref — secret refs to resolve before exec.
  sealed_input_modes  map[NAME]mode — "env" (default) or "file".
                      file mode writes the value to a 0600 temp
                      file under a per-action sealed-input dir
                      and exports the path as $NAME.
  sealed_outputs      map[NAME]ref — destinations to capture
                      after exec. Action writes the value to
                      $MU_SEALED_OUT_DIR/<NAME>; mu routes
                      through the scheme's plugin's store_secret.

  Plugins are responsible for forwarding these fields onto the
  ActionSpecs they emit (TargetInfo carries them; ActionSpec
  honors them at exec time).

ACTION SPEC FIELDS

  id                  Unique within the subgraph.
  command             []string — the command to execute.
  inputs              map[name]path — input files (paths resolved to CAS digests).
  outputs             []string — declared output file paths.
  depends_on          []string — IDs of actions this depends on (within subgraph).
  env                 map[string]string — environment variables (optional).
  sealed_inputs       map[NAME]ref — secret refs, resolved at runtime (optional).
  sealed_input_modes  map[NAME]mode — "env" (default) or "file" (optional).
  sealed_outputs      map[NAME]ref — destinations for $MU_SEALED_OUT_DIR/<NAME>
                      capture (optional). Forces impure.
  sealed_output_modes map[NAME]mode — store mode per output:
                      "create" | "overwrite" | "create_if_absent".
                      Default "overwrite".
  network             bool — whether action needs network (honor system).
  work_dir            string — working directory relative to project root (optional).
  impure              bool — skip CAS cache (optional; forced true
                      when sealed_outputs is non-empty).
  timeout_s           int — per-attempt wall-clock timeout in seconds (optional).
  retries             int — additional attempts on failure (network actions only).
  retry_backoff_ms    int — sleep between retries.

INTER-ACTION REFERENCES

  Inputs can reference outputs of other actions in the same subgraph:
    "inputs": {"lib.a": "{action:compile-lib}"}
  mu resolves these to the actual output digests after execution.

CROSS-TARGET ARTIFACTS

  A plan request includes "deps", one entry per listed dependency:
    "deps": [{"target": "//lib/crypto",
              "artifacts": {"go_library": "lib/crypto/libcrypto.a"}}]
  The artifacts map comes from each dep's declared_outputs (artifact-type
  name → project-relative path). To consume one, declare the path as a
  normal input:
    "inputs": {"cryptolib": "lib/crypto/libcrypto.a"}
  mu detects that the path is produced by an already-planned action,
  defers the input digest, and injects an implicit DependsOn edge to the
  producing action so the consumer runs after the producer.

  See 'mu guide pudl' for the terraform → pudl ingest end-to-end example.
