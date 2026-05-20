# void — Build Result Webhook Plugin

Posts build manifests to a void server webhook endpoint after each build.

## Configuration

```cue
plugins: [{ name: "void", script: "plugins/void/plugin.bb" }]

advice: [{
    plugin: "void"
    phases: ["after-build"]
    config: {
        webhook_url: "http://void-host:8080/webhook/myns/myrepo/mu-build"
    }
    sealed_inputs: {
        hmac_secret: "pass:void/webhook-hmac"
    }
}]
```

## What it sends

The full build manifest plus git context as a JSON POST body:

```json
{
  "version": 1,
  "type": "mu.build.manifest/v1",
  "timestamp": "2026-05-20T...",
  "duration_s": 12.3,
  "targets": [{"name": "//cmd/server", "toolchain": "go"}],
  "actions": [{"id": "//cmd/server:build", "cached": false, "exit_code": 0}],
  "summary": {"completed": 3, "cached": 2, "failed": 0, "cancelled": 0},
  "mu_context": {
    "project_root": "/path/to/project",
    "targets": ["//cmd/server"],
    "git_sha": "abc123",
    "git_branch": "main",
    "git_dirty": false
  }
}
```

## Authentication

When `hmac_secret` is provided, signs the payload with HMAC-SHA256 and
sends the signature in the `X-Hub-Signature-256` header (GitHub-compatible
format).
