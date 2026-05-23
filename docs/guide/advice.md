mu guide advice — build lifecycle observers

WHAT ADVICE IS

  Advice is mu's AOP-inspired mechanism for plugins to observe build events
  without affecting build outcomes. Advice is non-fatal: errors are logged
  to stderr but never fail the build.

  Use cases:
    - Post build results to a webhook (void plugin)
    - Send Slack/email notifications on failure
    - Update a dashboard or metrics system
    - Trigger downstream pipelines

PHASES

  Currently supported:
    after-build    Called after all actions complete (success or failure)

  The manifest includes targets, actions, cache hits, and timing.
  The advise_context includes git metadata (sha, branch, dirty).

CONFIGURATION

  Advice is declared in mu.cue at the project level:

    advice: [{
        plugin: "void"
        phases: ["after-build"]
        config: {
            webhook_url: "http://void:8080/webhook/ns/repo/mu-build"
        }
        sealed_inputs: {
            hmac_secret: "pass:void/webhook-hmac"
        }
    }]

  - plugin: name of the plugin (must be in plugins array)
  - phases: which lifecycle phases to call this plugin for
  - config: passed as advise_config in the request
  - sealed_inputs: resolved at runtime, passed as secrets

PLUGIN PROTOCOL

  Plugins declare advice capability during discover:

    → {"name": "void", "version": "0.1.0", "protocol_version": 1,
       "capabilities": ["discover", "advise"],
       "advise_phases": ["after-build"]}

  The coordinator calls advise after the build:

    ← {"method": "advise", "phase": "after-build",
       "manifest": {full build manifest},
       "advise_context": {"project_root": "...", "targets": [...],
                           "duration_s": 12.3, "git_sha": "abc123",
                           "git_branch": "main", "git_dirty": false},
       "advise_config": {from advice[].config},
       "secrets": {resolved sealed_inputs}}
    → {"ok": true}

  Timeout: 30 seconds. Errors in one advice plugin don't block others.

BUNDLED ADVICE PLUGINS

  void    Posts build manifest to a void server webhook endpoint.
          Supports HMAC-SHA256 signing (GitHub-compatible X-Hub-Signature-256).
          See: mu guide plugin void
