mu guide plugin terraform — Terraform convergence plugin

Manages Terraform-provisioned infrastructure: init, plan, and apply.

USAGE IN mu.cue

  targets: [{
    target: "//infra/vpc"
    toolchain: "terraform"
    sources: ["infra/vpc/*.tf"]
    config: {
      dir: "infra/vpc"
      auto_approve: true
    }
  }]

CONFIG FIELDS

  dir              Terraform working directory (default: ".").
  var_file         Path to a .tfvars variable file (optional).
  backend_config   Map of backend config key=value pairs (optional).
  auto_approve     Include apply step (default: true).
                   When false, only init + plan are run.
  parallelism      Max concurrent Terraform operations (optional).
  emit_state       Emit terraform state + outputs as JSON (default: true).
                   Produces state.json via `terraform show -json` and
                   outputs.json via `terraform output -json`, declared as
                   artifact types `terraform_state` and `terraform_outputs`
                   for downstream consumers (e.g. pudl).
  binary           Override the CLI to invoke. Accepts a command name or
                   absolute path. If unset, prefers `tofu` (OpenTofu)
                   when on PATH, else falls back to `terraform`.
  sealed_output_outputs  Capture-mode map: sealed_output NAME -> terraform
                         output name. See "Sealed outputs" below.

EXAMPLES

  Basic apply:
    {"dir": "infra/vpc"}

  Plan only (no apply):
    {"dir": "infra/vpc", "auto_approve": false}

  With variables and backend config:
    {"dir": "infra/vpc", "var_file": "prod.tfvars",
     "backend_config": {"bucket": "my-tf-state", "key": "vpc/terraform.tfstate"}}

OBSERVATION (DRIFT DETECTION)

  mu observe //infra/vpc

  Runs 'terraform init' then 'terraform plan -detailed-exitcode' to
  detect drift. Exit code 0 means no changes, exit code 2 means changes
  detected. Returns plan output as observation data.

ACTIONS GENERATED

  init           Runs 'terraform init' with backend config.
  plan           Runs 'terraform plan'. Depends on init.
                 Produces tfplan binary plan file.
  apply          Runs 'terraform apply tfplan'. Depends on plan.
                 Only generated when auto_approve is true.
  show           Runs 'terraform show -json' and 'terraform output -json',
                 writing state.json and outputs.json. Depends on apply
                 (or plan in plan-only mode). Only generated when
                 emit_state is true.
  fetch-secrets  Runs 'terraform output -raw' per sealed_output_outputs
                 entry into $MU_SEALED_OUT_DIR/NAME (chmod 0600). Depends
                 on apply. Only generated when sealed_outputs is set.

All actions are marked impure and require network access. Sealed inputs
declared at target-level are forwarded to every action.

DECLARED OUTPUTS (when emit_state is true)

  terraform_state     state.json   Full resource graph from `terraform show -json`
  terraform_outputs   outputs.json Declared outputs from `terraform output -json`

Downstream targets can depend on these via deps and read the JSON artifacts.

SEALED INPUTS (cloud credentials)

  Forward provider credentials and secrets via target.sealed_inputs:

    "sealed_inputs": {
      "AWS_ACCESS_KEY_ID":     "pass:aws/prod/key",
      "AWS_SECRET_ACCESS_KEY": "pass:aws/prod/secret"
    }

  Single-line credentials use env delivery (the default). Multi-line or
  large secrets — GCP service-account JSON, AWS web-identity tokens —
  should declare file mode so the bytes are written to a 0600 temp file
  and $NAME holds the path:

    "sealed_inputs":      {"GOOGLE_APPLICATION_CREDENTIALS": "pass:raw:gcp/sa-json"},
    "sealed_input_modes": {"GOOGLE_APPLICATION_CREDENTIALS": "file"}

  terraform's google provider then reads the JSON from $NAME (path) per
  its standard env-var contract. Sealed inputs are forwarded to every
  action (init, plan, apply, show, fetch-secrets).

SEALED OUTPUTS (sensitive terraform outputs -> secret backend)

  Use when a terraform output value is itself sensitive — RDS master
  password, generated API token, IAM access keys — and should land in
  a secret backend (pass / vault / sops) instead of outputs.json in CAS.

  Pair target.sealed_outputs with config.sealed_output_outputs (NAME ->
  terraform output name):

    targets: [{
      target: "//infra/rds"
      toolchain: "terraform"
      sources: ["infra/rds/*.tf"]
      config: {
        dir: "infra/rds"
        sealed_output_outputs: DBPASS: "db_master_password"
      }
      sealed_inputs: {
        AWS_ACCESS_KEY_ID: "pass:aws/key"
        AWS_SECRET_ACCESS_KEY: "pass:aws/secret"
      }
      sealed_outputs: DBPASS: "pass:rds/master-password"
      sealed_output_modes: DBPASS: "create_if_absent"
    }]

  The fetch-secrets action runs `terraform output -raw db_master_password`
  and pipes the value into $MU_SEALED_OUT_DIR/DBPASS. The runner then
  routes the bytes through the configured provider's store_secret.

  Constraints
    - Keys in sealed_output_outputs MUST exactly match sealed_outputs.
      The plugin rejects mismatches at plan time.
    - sealed_outputs require auto_approve:true (no apply -> no outputs).
    - The matching ref must appear in secrets.writable_refs in the
      project mu.cue.
    - Mark each terraform output as `sensitive = true` so terraform's
      log output redacts it; the value still flows through `output -raw`
      correctly.
    - Actions with sealed_outputs are forced impure — the cache is
      skipped so store_secret always runs.

CAPABILITIES

  discover, plan, observe
