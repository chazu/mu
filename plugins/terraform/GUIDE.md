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

  tf-init     Runs 'terraform init' with backend config.
  tf-plan     Runs 'terraform plan'. Depends on tf-init.
              Uses -detailed-exitcode for change detection.
  tf-apply    Runs 'terraform apply -auto-approve'. Depends on tf-plan.
              Only generated when auto_approve is true.

All actions are marked impure and require network access.

CAPABILITIES

  discover, plan, observe
