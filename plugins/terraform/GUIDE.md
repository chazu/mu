mu guide plugin terraform — Terraform convergence plugin

Manages Terraform-provisioned infrastructure: init, plan, and apply.

USAGE IN mu.json

  {
    "target": "//infra/vpc",
    "toolchain": "terraform",
    "sources": ["infra/vpc/*.tf"],
    "config": {
      "dir": "infra/vpc",
      "auto_approve": true
    }
  }

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

  init        Runs 'terraform init' with backend config.
  plan        Runs 'terraform plan'. Depends on init.
              Produces tfplan binary plan file.
  apply       Runs 'terraform apply tfplan'. Depends on plan.
              Only generated when auto_approve is true.
  show        Runs 'terraform show -json' and 'terraform output -json',
              writing state.json and outputs.json. Depends on apply
              (or plan in plan-only mode). Only generated when
              emit_state is true.

All actions are marked impure and require network access.

DECLARED OUTPUTS (when emit_state is true)

  terraform_state     state.json   Full resource graph from `terraform show -json`
  terraform_outputs   outputs.json Declared outputs from `terraform output -json`

Downstream targets can depend on these via deps and read the JSON artifacts.

CAPABILITIES

  discover, plan, observe
