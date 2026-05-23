mu guide observe — drift detection

The observe command asks plugins to report the current state of resources.
Plugins act as sensors — they report what exists, not whether it's correct.
Convergence decisions are made by pudl or the operator.

USAGE

  mu observe [flags] <target>...

FLAGS

  --json      Output as JSON array of ObserveResult objects.
  --ndjson    Output flattened records, one per line (for piping to pudl).
  --config    Path to mu.cue.
  --verbose   Show plugin I/O.

EXAMPLES

  mu observe //infra/aws-inventory
  mu observe --json //infra/aws-inventory
  mu observe --ndjson //infra/aws-inventory | pudl import --stdin

HOW IT WORKS

  1. mu loads the target from mu.cue.
  2. If the target has sealed_inputs, secrets are resolved first.
  3. mu sends an "observe" request to the target's plugin with:
     - target info (name, toolchain, config)
     - resolved secrets (never logged or cached)
  4. The plugin queries the real system (AWS API, filesystem, etc.)
     and returns: {"current": {"records": [...]}}
  5. Each record should include "_schema" for pudl routing:
     {"_schema": "aws.ec2.instance", "instance_id": "i-abc", ...}

OUTPUT FORMATS

  Plain text (default):
    //target  {"records":[...]}

  JSON (--json): Full ObserveResult array with target context:
    [{"target": "//target", "current": {"records": [...]}}]

  NDJSON (--ndjson): One record per line, for streaming into pudl:
    {"_schema":"aws.ec2.instance","instance_id":"i-abc",...}
    {"_schema":"aws.ec2.vpc","vpc_id":"vpc-123",...}

WRITING AN OBSERVE PLUGIN

  Add "observe" to your plugin's capabilities list in discover, then
  handle the "observe" method:

    "discover" {"capabilities" ["discover" "plan" "observe"] ...}

    "observe"  (let [config (get-in req ["target" "config"])
                     secrets (get req "secrets")]
                 ;; Query the real system using config + secrets.
                 {"current" {"records" [{...} {...}]}})

  Record conventions:
  - Include "_schema" field for pudl routing (e.g. "aws.ec2.instance").
  - Return arrays even for single resources.
  - Return {"error": "message"} on failure.
