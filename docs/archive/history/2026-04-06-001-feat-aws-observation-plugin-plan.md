---
title: "feat: Add AWS observation plugin"
type: feat
status: completed
date: 2026-04-06
origin: docs/brainstorms/2026-04-06-aws-observation-plugin.md
---

# feat: Add AWS observation plugin

## Overview

Add a new `aws` plugin to mu that observes AWS resource state via the AWS CLI. V1 covers EC2 instances, VPCs, and subnets — all regional resources that follow the same describe pattern. The plugin follows the host plugin's Babashka + NDJSON architecture.

## Problem Frame

mu can observe host-level state (via SSH) but has no visibility into AWS cloud resources. This plugin enables mu to observe AWS infrastructure state so pudl can reason about drift. (see origin: `docs/brainstorms/2026-04-06-aws-observation-plugin.md`)

## Requirements Trace

- R0. Discover declares `["discover", "observe"]` only
- R1. `profile` required in target config, error if omitted
- R2. Works with any `~/.aws/config` profile; surfaces auth errors as-is
- R3. `region` required, error if omitted
- R3a. Every CLI call includes `--profile` and `--region`
- R4. `resources` list selects which types to observe
- R5. Error on empty/missing/unknown `resources`
- R6. Records use `_schema` prefixed `aws.`
- R7. `account` + `region` on every record; STS called once, cached; error if STS fails
- R8. Returns `{"current": {"records": [...]}}`
- R8a. Must paginate fully (no truncation)
- R9. V1: EC2 instances, VPCs, subnets
- R10. Easy to add resource types
- R12. Validate AWS CLI v2 on PATH at startup

## Scope Boundaries

- Observe only, no plan/converge
- No S3, IAM, RDS, EKS in V1 (see origin)
- No inline credentials — profiles only
- No multi-region per target
- No resource filtering within a type

## Context & Research

### Relevant Code and Patterns

- `plugins/host/plugin.bb` — primary pattern: Babashka NDJSON loop, discover + observe dispatch, `process/sh` for subprocesses
- `plugins/host/mu.json` — plugin manifest structure: `{"plugin": {"entrypoint": "plugin.bb", "toolchain": "bb", "files": [...]}}`
- `plugins/host/gather.sh` — NDJSON output with `_schema` fields (not needed here since we're calling AWS CLI directly, not piping a script over SSH)
- `internal/plugin/process.go:67` — 1MB scanner buffer limit for NDJSON responses
- `internal/plugin/protocol.go:44-51` — ObserveResponse: `Current map[string]any` + `Error string`
- `internal/plugin/manager.go:157-160` — capability gate: returns empty response if plugin doesn't declare "observe"
- `internal/coordinator/coordinator.go:422-437` — how coordinator calls `mgr.Observe()` with target info and secrets

### Key Technical Findings

- **AWS CLI v2 auto-paginates by default** — no manual NextToken handling needed. Just call `aws ec2 describe-instances` and all pages are merged automatically. `--no-paginate` *disables* this (don't use it).
- **JMESPath `--query`** — client-side field selection reduces output size. Full payload still transfers from AWS, but the JSON the plugin parses is smaller.
- **Response sizes for V1 types** — EC2 (50 instances): ~250-400KB, VPCs (20): ~10-20KB, Subnets (100): ~50-100KB. Total ~300-520KB, well under the 1MB limit. For very large accounts (500+ instances), could approach the limit. Use `--query` to extract only needed fields as a safety margin.
- **STS get-caller-identity** — always succeeds (cannot be denied by IAM policy), returns ~150 bytes.
- **Babashka concurrency** — `future` and `pmap` available for parallel CLI calls.

## Key Technical Decisions

- **Single `plugin.bb` with inline gather functions**: No separate gather script needed. The host plugin uses `gather.sh` because it pipes a script over SSH — the AWS plugin calls `aws` CLI directly from Babashka. Each resource type is a function that returns a vector of records.
- **Use `--query` JMESPath to extract specific fields**: Reduces response size and gives us control over the schema shape. Raw `describe-instances` output is 3-8KB per instance; with `--query` we extract only the fields useful for drift detection, ~500 bytes per instance.
- **Sequential CLI calls (no concurrency in V1)**: With 3 resource types, total observe time is ~2-4 seconds (STS + 3 describe calls). Babashka `future` concurrency is a natural optimization for when more resource types are added, but premature for V1.
- **Partial failure: fail the entire observe**: If any resource type fails, return an error. This matches the host plugin pattern and is simpler than partial results. Partial failure semantics can be revisited when more resource types make selective failure more likely.
- **Canonical resource type names**: `"ec2"` → `aws.ec2.instance`, `"vpc"` → `aws.ec2.vpc`, `"subnet"` → `aws.ec2.subnet`. All V1 types map to EC2 API calls, so the `aws.ec2.*` prefix is accurate.

## Open Questions

### Resolved During Planning

- **Pagination handling**: AWS CLI v2 auto-paginates by default. No manual handling needed for R8a — just don't pass `--no-paginate`.
- **Response size risk**: V1 resource types total ~300-520KB for a moderate account, well under the 1MB limit. Using `--query` to extract specific fields adds further safety margin.
- **File structure**: Single `plugin.bb` (no gather script). All resource-type logic is inline Babashka functions.
- **Partial failure**: Fail the entire observe. Simpler, matches host plugin pattern.
- **Resource names**: `ec2`, `vpc`, `subnet` as config values mapping to `aws.ec2.instance`, `aws.ec2.vpc`, `aws.ec2.subnet` schemas.

### Deferred to Implementation

- Exact `--query` JMESPath expressions per resource type — these should be refined by looking at actual AWS output during implementation
- Whether `describe-instances` Reservations nesting needs flattening or the query handles it — test with real output

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

```
Plugin Lifecycle:
  stdin → JSON request → dispatch on "method" → JSON response → stdout

Discover:
  1. Check `aws --version` → error if not v2
  2. Return name, capabilities, config_schema

Observe:
  1. Validate config: profile, region, resources (all required)
  2. Validate resource names against known set
  3. Call STS get-caller-identity → extract account ID
  4. For each resource type in config.resources:
     a. Call aws ec2 describe-* with --profile, --region, --query, --output json
     b. Parse JSON, attach _schema + account + region to each record
     c. Append to records vector
  5. Return {"current": {"records": [all records]}}
  On any error → return {"error": "descriptive message"}

Resource Type Registry (map of config name → gather function):
  "ec2"    → calls describe-instances, extracts key fields
  "vpc"    → calls describe-vpcs, extracts key fields
  "subnet" → calls describe-subnets, extracts key fields
```

## Implementation Units

- [x] **Unit 1: Plugin scaffold with discover and CLI validation**

**Goal:** Create the plugin directory, mu.json manifest, and plugin.bb with discover method and AWS CLI v2 validation.

**Requirements:** R0, R12

**Dependencies:** None

**Files:**
- Create: `plugins/aws/mu.json`
- Create: `plugins/aws/plugin.bb`

**Approach:**
- Follow `plugins/host/mu.json` structure exactly: `{"plugin": {"entrypoint": "plugin.bb", "toolchain": "bb", "files": ["plugin.bb"]}}`
- Implement the NDJSON stdin/stdout loop (same as host plugin lines 93-99)
- Implement `handle-discover`: check `aws --version` via `process/sh`, verify output starts with "aws-cli/2.", return error if not found or wrong version
- Config schema: `profile` (string, required), `region` (string, required), `resources` (array of string, required)
- Capabilities: `["discover", "observe"]`
- Produces: `["aws_state"]`

**Patterns to follow:**
- `plugins/host/plugin.bb` lines 20-34 (discover), 86-99 (dispatch + NDJSON loop)
- `plugins/host/mu.json` (manifest structure)

**Test scenarios:**
- Happy path: discover returns correct name, version, capabilities, config_schema
- Error path: `aws` not on PATH → discover returns error with install instructions
- Error path: AWS CLI v1 installed → discover returns error mentioning v2 requirement

**Verification:**
- Plugin can be registered in mu.json and mu starts it without error
- Discover response matches expected schema

---

- [x] **Unit 2: Config validation and STS identity resolution**

**Goal:** Implement config validation (profile, region, resources) and STS get-caller-identity call with caching.

**Requirements:** R1, R3, R4, R5, R7

**Dependencies:** Unit 1

**Files:**
- Modify: `plugins/aws/plugin.bb`

**Approach:**
- Add `validate-config` function: check that profile, region, and resources are present and non-empty. Check each resource name against a known set (`#{"ec2" "vpc" "subnet"}`). Return error map on failure.
- Add `get-account-id` function: call `aws sts get-caller-identity --profile <p> --region <r> --output json`, parse JSON, extract `Account` field. Cache result in an atom for the process lifetime (STS identity doesn't change per profile+region combo within a session).
- Error handling: if STS fails, return `{"error": "..."}` with the raw stderr from the AWS CLI.

**Patterns to follow:**
- `plugins/host/plugin.bb` lines 65-70 (config extraction and validation)

**Test scenarios:**
- Happy path: valid config with profile, region, resources=["ec2"] passes validation
- Error path: missing `profile` → error mentioning profile is required
- Error path: missing `region` → error mentioning region is required
- Error path: empty `resources` list → error
- Error path: unknown resource type `"rds"` → error listing valid types
- Happy path: STS call returns account ID
- Error path: STS call fails (expired credentials) → error with AWS CLI message

**Verification:**
- Invalid configs produce clear, actionable error messages
- STS result is cached (second call doesn't shell out again)

---

- [x] **Unit 3: EC2 instance observation**

**Goal:** Implement the EC2 instance gather function that calls describe-instances and returns structured records.

**Requirements:** R3a, R6, R7, R8, R8a, R9

**Dependencies:** Unit 2

**Files:**
- Modify: `plugins/aws/plugin.bb`

**Approach:**
- Add `gather-ec2` function: calls `aws ec2 describe-instances --profile <p> --region <r> --output json --query "Reservations[].Instances[].[InstanceId,InstanceType,State.Name,VpcId,SubnetId,PrivateIpAddress,PublicIpAddress,ImageId,Tags,SecurityGroups[].GroupId,IamInstanceProfile.Arn]"` (refine query during implementation)
- Parse JSON output, map each instance to a record with `_schema: "aws.ec2.instance"`, `account`, `region`, and extracted fields
- Handle the Reservations[].Instances[] nesting (JMESPath flattens this)
- Auto-pagination is handled by AWS CLI v2 default behavior

**Fields to extract per instance:**
| Field | Source | Purpose |
|-------|--------|---------|
| `instance_id` | InstanceId | Primary key |
| `instance_type` | InstanceType | Drift detection |
| `state` | State.Name | Lifecycle state |
| `vpc_id` | VpcId | Network placement |
| `subnet_id` | SubnetId | Network placement |
| `private_ip` | PrivateIpAddress | Network identity |
| `public_ip` | PublicIpAddress | Network identity |
| `image_id` | ImageId | AMI drift |
| `tags` | Tags | Tag drift |
| `security_groups` | SecurityGroups[].GroupId | SG membership |
| `iam_profile` | IamInstanceProfile.Arn | IAM drift |

**Patterns to follow:**
- `plugins/host/gather.sh` lines 22-23 (NDJSON record with `_schema` and identity fields)

**Test scenarios:**
- Happy path: describe-instances returns 2 instances → 2 records with correct `_schema`, `account`, `region`, and all extracted fields
- Happy path: no instances in region → empty records for this type (not an error)
- Error path: describe-instances fails (permission denied) → observe returns error
- Edge case: instance with no public IP → `public_ip` is null/absent
- Edge case: instance with no tags → `tags` is empty array

**Verification:**
- Each record has `_schema: "aws.ec2.instance"`, `account`, `region`, and instance_id at minimum
- Records are valid JSON parseable by pudl

---

- [x] **Unit 4: VPC and subnet observation**

**Goal:** Implement VPC and subnet gather functions following the same pattern as EC2.

**Requirements:** R3a, R6, R7, R8, R8a, R9

**Dependencies:** Unit 3 (to reuse the established pattern)

**Files:**
- Modify: `plugins/aws/plugin.bb`

**Approach:**
- Add `gather-vpc` function: `aws ec2 describe-vpcs --profile <p> --region <r> --output json --query "Vpcs[].[VpcId,CidrBlock,State,IsDefault,Tags,InstanceTenancy]"` (refine during implementation)
- Add `gather-subnet` function: `aws ec2 describe-subnets --profile <p> --region <r> --output json --query "Subnets[].[SubnetId,VpcId,CidrBlock,AvailabilityZone,State,MapPublicIpOnLaunch,AvailableIpAddressCount,Tags]"` (refine during implementation)
- Same record structure: `_schema`, `account`, `region`, plus type-specific fields

**Fields per type:**

VPC: `vpc_id`, `cidr_block`, `state`, `is_default`, `tags`, `instance_tenancy`

Subnet: `subnet_id`, `vpc_id`, `cidr_block`, `availability_zone`, `state`, `map_public_ip_on_launch`, `available_ip_count`, `tags`

**Patterns to follow:**
- Unit 3's gather-ec2 function (same CLI invocation + parsing pattern)

**Test scenarios:**
- Happy path: describe-vpcs returns 2 VPCs → 2 records with `_schema: "aws.ec2.vpc"`
- Happy path: describe-subnets returns 5 subnets → 5 records with `_schema: "aws.ec2.subnet"`, each with `vpc_id` reference
- Happy path: no VPCs/subnets → empty records for these types
- Error path: describe-vpcs fails → entire observe errors
- Edge case: VPC with no tags → `tags` is empty array

**Verification:**
- VPC records have `vpc_id` and `cidr_block`; subnet records have `subnet_id`, `vpc_id`, and `cidr_block`
- Schema names are `aws.ec2.vpc` and `aws.ec2.subnet`

---

- [x] **Unit 5: Observe dispatch and integration**

**Goal:** Wire up the observe method to validate config, call STS, dispatch to gather functions, and assemble the final response.

**Requirements:** R1, R3, R3a, R4, R5, R7, R8, R10

**Dependencies:** Units 2, 3, 4

**Files:**
- Modify: `plugins/aws/plugin.bb`

**Approach:**
- Add `handle-observe` function following the host plugin pattern:
  1. Extract target config and validate
  2. Get account ID (cached STS call)
  3. Build resource-type dispatch map: `{"ec2" gather-ec2, "vpc" gather-vpc, "subnet" gather-subnet}`
  4. For each requested resource type, call its gather function with profile, region, account
  5. Concatenate all records into a single vector
  6. Return `{"current" {"records" all-records}}`
- Wire `handle-observe` into the NDJSON dispatch (alongside discover)
- R10 extensibility: adding a resource type means adding a function and one entry to the dispatch map

**Patterns to follow:**
- `plugins/host/plugin.bb` lines 65-82 (handle-observe structure)
- `plugins/host/plugin.bb` lines 86-89 (dispatch in handle-request)

**Test scenarios:**
- Happy path: target with `resources: ["ec2", "vpc"]` → records from both types, all with matching `account` and `region`
- Happy path: target with `resources: ["subnet"]` → only subnet records
- Integration: full observe with all 3 resource types → correct record count, all schemas present
- Error path: STS fails → error returned, no partial records
- Error path: one resource type fails → entire observe returns error

**Verification:**
- `mu observe` against a configured target produces structured JSON output with records
- Adding a hypothetical 4th resource type requires only adding a gather function and a dispatch map entry

## System-Wide Impact

- **Interaction graph:** The plugin is self-contained. It only interacts with mu via the NDJSON protocol (stdin/stdout). No callbacks, middleware, or shared state.
- **Error propagation:** Plugin errors surface as `ObserveResult.Error` in the coordinator, which reports them to the user. No cascading failures.
- **Unchanged invariants:** No changes to mu coordinator, protocol types, or existing plugins. The aws plugin is additive.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| 1MB response limit hit by large accounts (500+ instances) | JMESPath `--query` extracts only needed fields (~500 bytes/instance). 500 instances ≈ 250KB. Would need ~2000 instances to approach 1MB. Acceptable for V1. |
| AWS CLI not installed on user's machine | R12: validate at discover time with clear error message |
| Expired SSO/MFA tokens cause confusing errors | R2: surface raw AWS CLI error message. User must refresh tokens before running observe. |
| Profile name collision with environment variables | R3a: always pass `--profile` explicitly, never rely on env defaults |

## Sources & References

- **Origin document:** [docs/brainstorms/2026-04-06-aws-observation-plugin.md](docs/brainstorms/2026-04-06-aws-observation-plugin.md)
- Host plugin pattern: `plugins/host/plugin.bb`, `plugins/host/mu.json`, `plugins/host/gather.sh`
- Plugin protocol: `internal/plugin/protocol.go` (ObserveResponse lines 44-51)
- Process management: `internal/plugin/process.go` (1MB buffer line 67, 5min timeout line 190)
- AWS CLI v2 auto-pagination: default behavior, no `--no-paginate` needed
