mu guide pudl — how mu and pudl work together

mu and pudl are decoupled tools that communicate through mu.cue.

  pudl: defines desired state (CUE), observes actual state, computes drift.
  mu:   takes desired-state targets and converges them using plugins.

Neither tool imports or depends on the other.

WORKFLOW

  1. Define desired state in CUE (pudl's schema system):

     package definitions
     nginx_conf: file.#Config & {
         path:    "/etc/nginx/nginx.conf"
         content: "server { listen 80; }"
         mode:    "0644"
     }

  2. Observe actual state and check for drift:

     pudl import --path /etc/nginx/nginx.conf
     pudl drift check nginx_conf

  3. Export drifted resources as a mu config:

     pudl export-actions --definition nginx_conf > converge.json

     This produces a mu.cue with desired-state targets:
     {
       "targets": [{
         "target": "//nginx_conf",
         "toolchain": "file",
         "config": {"path": "/etc/nginx/nginx.conf", "content": "...", "mode": "0644"}
       }]
     }

  4. Converge with mu:

     mu build --config converge.json //nginx_conf

  5. Verify convergence (the ACUTE loop):

     mu observe --ndjson //nginx_conf | pudl import --stdin
     pudl drift check nginx_conf  # should report no drift

OBSERVATION PIPELINE

  mu observe --ndjson <targets> | pudl import --stdin

  mu core emits one record per target (a "records" array with --json, or
  one JSON object per line with --ndjson). Any _schema tagging that pudl
  routes on is a plugin/pudl-side convention, not something mu core adds.
  pudl routes each record by schema to the appropriate CUE definition for
  comparison.

RESOURCE TYPE MAPPING

  pudl maps CUE schema prefixes to mu toolchain names:

    file.*, config.*            → file
    ec2.*, s3.*, iam.*, aws.*   → aws
    k8s.*, kubernetes.*         → k8s
    (unknown)                   → generic

DATA IMPORT (mu → pudl)

  Beyond the drift loop above, mu plugins can also produce data that
  flows into pudl's catalog. Plugins optionally declare a CUE schema
  for their output so pudl classifies the data under a meaningful
  type instead of the catchall pudl/core.#Item.

  See 'mu guide plugins' (OUTPUT SCHEMAS section) for the plugin-side
  contract, or docs/plugin-output-schemas.md for the full guide.

  On import, pudl auto-detects an envelope JSON with shape
  {"schema": {...}, "definitions": [...], "data": <payload>} and
  classifies the data under the declared schema. Raw JSON (no
  envelope) can still be typed explicitly via
  'pudl import --schema mu/aws@v1#EC2Instance'. Items can satisfy
  multiple schemas; unresolved refs are tagged for later upgrade via
  'pudl reclassify'.

KEY DESIGN PRINCIPLE

  pudl emits desired state, not drift diffs. The file plugin receives
  {"path": "...", "content": "..."} — it doesn't know about pudl, CUE,
  or drift reports. It just makes the file match the config. Any mu plugin
  works whether the target came from pudl or a hand-written mu.cue.

PUDL AS A BUILD TARGET

  pudl can also run inside the build graph as a consumer of another
  target's declared outputs. The common case: run terraform (or any
  toolchain that emits state/artifacts) and feed the result into pudl
  in a single 'mu build' invocation.

    {
      "targets": [
        {
          "target": "//infra/vpc",
          "toolchain": "terraform",
          "sources": ["infra/vpc/*.tf"],
          "config": {"dir": "infra/vpc"}
        },
        {
          "target": "//pudl/vpc-catalog",
          "toolchain": "pudl",
          "deps": ["//infra/vpc"],
          "config": {"from": "terraform_state"}
        }
      ]
    }

  The terraform plugin declares outputs:

    "declared_outputs": {
      "terraform_state":   "infra/vpc/state.json",
      "terraform_outputs": "infra/vpc/outputs.json"
    }

  mu threads those into the pudl plan request as:

    "deps": [{"target": "//infra/vpc",
              "artifacts": {"terraform_state": "infra/vpc/state.json",
                            "terraform_outputs": "infra/vpc/outputs.json"}}]

  A pudl plugin that declares an action with
  inputs = {"state": "infra/vpc/state.json"} gets an implicit
  DependsOn edge to the terraform show action, so the DAG runs the
  producer first and the consumer after.

  See 'mu guide protocol' for the declared_outputs / deps[].artifacts
  contract and how to consume cross-target artifacts from a plugin.
