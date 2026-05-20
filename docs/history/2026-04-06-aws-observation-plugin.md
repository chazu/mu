---
date: 2026-04-06
topic: aws-observation-plugin
---

# AWS Observation Plugin

## Problem Frame

mu can observe host-level state via the `host` plugin (SSH + gather script), but has no visibility into AWS cloud resources. To manage infrastructure as part of mu's DAG, we need a plugin that can observe the current state of AWS resources across accounts/roles and emit structured records for pudl to reason about.

## Requirements

**Plugin Protocol**
- R0. The plugin's discover response declares `capabilities: ["discover", "observe"]` only. No plan method is implemented in V1.

**Authentication**
- R1. Target config specifies an AWS CLI profile name (`profile` field, required). If `profile` is omitted, the plugin returns an error — it must never fall back to the default credential chain silently.
- R2. The plugin must work with any profile configured in the user's `~/.aws/config`, including profiles that use `role_arn` + `source_profile` (AWS handles the assume-role internally). Auth failures (expired SSO token, MFA required) are surfaced as errors with the raw AWS CLI error message. The plugin does not attempt token refresh or interactive auth.
- R3. Target config specifies a `region` (required). If `region` is omitted, the plugin returns an error. The plugin uses `--region <value>` for all regional calls. Global services (IAM, S3 bucket listing) use a fixed region or omit `--region` as appropriate.
- R3a. Every AWS CLI invocation must unconditionally include both `--profile` and `--region` flags — never rely on environment defaults or implicit CLI config. This prevents credential bleed between targets in the long-lived plugin process.

**Resource Selection**
- R4. Target config includes a `resources` list specifying which resource types to observe (e.g., `["ec2", "s3", "vpc"]`). The plugin only queries the listed types.
- R5. If `resources` is empty or omitted, the plugin returns an error rather than silently observing nothing or everything. Unknown resource type names also produce an error listing valid types.

**Observe Output**
- R6. Each observed resource emits one NDJSON record with a `_schema` field prefixed `aws.` (e.g., `aws.ec2.instance`, `aws.s3.bucket`, `aws.vpc.vpc`, `aws.vpc.subnet`, `aws.iam.user`, `aws.iam.role`, `aws.iam.policy`).
- R7. Each record includes an `account` field (from STS get-caller-identity) and `region` field for identity. Global resources use `"region": "global"`. The STS call is made once per observe invocation and cached. If the STS call fails, the plugin returns an error for the entire observe rather than emitting records without identity.
- R8. The plugin returns `{"current": {"records": [...]}}` following the same pattern as the host plugin.
- R8a. For resource types whose AWS APIs paginate results, the plugin must iterate all pages. No silent truncation is permitted.

**Dependency Validation**
- R12. On startup (discover), the plugin verifies that the `aws` CLI is available on PATH and is v2 (`aws --version` starts with `aws-cli/2.`). If absent or wrong version, return an error with a clear message.

**Initial Resource Types (start small)**
- R9. V1 ships with support for: EC2 instances, VPCs, and subnets.
- R10. The plugin is designed so adding new resource types is straightforward (add a gather function per type).
- R11. S3, IAM, RDS, and EKS are deferred to a fast follow-up, not V1. S3 and IAM require special handling as global services (see Key Decisions).

## Success Criteria

- A target configured with `"toolchain": "aws"`, a profile, region, and resource list produces structured observation records
- Records are useful for pudl drift detection: they contain enough fields to identify the resource and its key properties
- Adding a new resource type requires only edits to the plugin's own source files (no changes to mu coordinator, protocol types, or project-level mu.json)

## Scope Boundaries

- Observe only — no plan/converge actions in V1
- No inline credential support (access key/secret key in config) — profiles only
- No multi-region in a single target — use separate targets per region
- No filtering within resource types (e.g., "only EC2 instances with tag X") in V1
- No secret resolution needed — AWS CLI profiles handle auth without mu's sealed_inputs
- S3 and IAM deferred from V1 — global services need separate design work
- No inline assume-role — cross-account access requires pre-configured profiles

## Key Decisions

- **Single plugin, not per-resource**: One `aws` plugin handles all resource types. Target config selects which types to observe. This keeps plugin registration simple and shares the auth flow.
- **AWS CLI profile for auth**: Leverages existing `~/.aws/config` rather than implementing STS assume-role directly. Simpler and works with SSO, MFA, federated roles — anything the AWS CLI already supports. Cross-account observation requires a pre-configured profile with the right role ARN — the plugin does not support inline assume-role.
- **Babashka plugin**: Follows the host plugin pattern. Uses `aws` CLI commands via shell rather than an AWS SDK, keeping the plugin lightweight and dependency-free beyond the AWS CLI itself. Note: this intentionally trades mu's hermeticity guarantee for convenience — the AWS CLI is not version-pinned by mu.
- **S3 and IAM deferred from V1**: Both are global services that conflict with the per-region target model. S3 bucket listing returns all buckets regardless of region; IAM ignores `--region` entirely. These require design decisions (global resource type handling, schema naming for IAM sub-types, managed vs. customer policy scoping) that are better addressed once the regional resource pattern is proven with EC2/VPC/subnets.

## Dependencies / Assumptions

- AWS CLI v2 must be installed and on PATH
- User has working profiles in `~/.aws/config`
- Babashka runtime available (already a mu toolchain)

## Outstanding Questions

### Deferred to Planning
- [Affects R6][Technical] Exact fields to include per resource type — should be determined by what `aws <service> describe-*` / `list-*` commands return and what's useful for drift detection. Define minimum identity fields per type before implementation.
- [Affects R9][Needs research] Whether to shell out to `aws` CLI per resource type or batch calls — investigate performance tradeoffs during planning. Consider using babashka `future`/`pmap` for concurrent calls.
- [Affects R10][Technical] Plugin file structure — single `plugin.bb` with inline gather functions, or separate gather scripts per resource type like host's `gather.sh`. Must include `mu.json` manifest per host plugin pattern.
- [Affects R8][Needs research] NDJSON response size limits — `process.go` scanner has a 1MB max line length. Check whether V1 resource types (EC2/VPC/subnet) could exceed this for large accounts. If so, may need to split observations or increase the buffer.
- [Affects R5][Technical] Define partial-failure semantics — if one resource type fails (e.g., AccessDenied) but others succeed, should the plugin emit partial results with error records or fail the entire observe?
- [Affects R4][Technical] Define canonical resource type names for the `resources` config list and map them to `_schema` values (e.g., `"ec2"` → `aws.ec2.instance`, `"vpc"` → `aws.vpc.vpc`, `"subnet"` → `aws.vpc.subnet`).

## Next Steps

-> `/ce:plan` for structured implementation planning
