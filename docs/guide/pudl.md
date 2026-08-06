mu guide pudl — how mu and pudl work together

mu and pudl are decoupled tools with a clear ownership boundary:

  pudl: model selection, observation, drift, value wiring, approvals, reports.
  mu:   plugin planning, action execution, caching, and secret-provider I/O.

Neither tool imports the other. PUDL renders temporary mu configuration and
exchanges versioned JSON results with the mu CLI.

PRIMARY WORKFLOW — PUDL OWNS THE LOOP

  # Observe one registered #SystemModel. No mutation.
  pudl run app

  # Observe exactly the named producer/consumer set in dependency order.
  pudl run-set network app

  # Close drift. PUDL renders desired sources and invokes mu internally.
  pudl run app --converge

  # Whole-set read-only preflight, then mutation.
  pudl run-set network app --converge

There is no separate `pudl drift` or `pudl export-actions` command. Those were
part of the retired pre-#SystemModel workflow. Do not manually synthesize an
intermediate config when PUDL owns the model run.

EXACT RUN-SETS

`pudl run-set <models...>` is closed and explicit. PUDL does not discover and
start an omitted producer. It rejects missing producers and cycles before any
member runs, orders producers first, and pins successful producer observations
for downstream consumers.

Without `--converge` all members are observe-only. With it, PUDL completes
read-only planning for the full set before the first mutation. Durable reports
are available through:

  pudl run-set report [run-set-id]
  pudl run-set resume <run-set-id>
  pudl run-set reject <run-set-id>

PLAIN VALUE BINDINGS

Plain bindings are scalar projections from PUDL catalog snapshots. A consumer
model names its producer, resource schema and identity, and RFC 6901 field path.
Both the consumer input and source schema field must declare
`@pudl(binding=plain)`. PUDL type-checks the elaborated model and persists the
producer run, snapshot, identity, path, age, and value digest as evidence.

SEALED VALUE BINDINGS

Secrets never take the catalog/plain path. PUDL-generated targets declare
provider refs and set:

  sealed_routing: "strict"

In strict mode target-level sealed maps define availability and policy, not
implicit grants. Plugin actions must explicitly claim the exact target ref and
effective mode for each sealed name they use. Every declared input must be
claimed at least once; every declared output exactly once. Undeclared claims,
unused declarations, ref/mode changes, and ambiguous outputs fail planning.

Mu does not resolve provider values during planning. It resolves each claimed
input immediately before action execution and re-checks
`secrets.writable_refs` before each provider write. PUDL persists only redacted
fingerprints, not values or provider refs. Sealed outputs are converge-only in
PUDL, and a mutating run-set containing one always pauses for exact-plan
approval:

  pudl run-set network app --converge
  pudl run-set resume <run-set-id>   # or reject

For each exact apply, PUDL rechecks the workspace-normalized approved plan,
then invokes `mu build --expect-plan-sha256 <raw-plan-digest>`. Mu compares a
single in-process plan before provider access and executes that same graph.
Resolved plugin/provider content identities are part of plan schema v2;
mutable command plugins are rejected on this guarded path.

STANDALONE MU OPERATIONS

Use mu directly when mu.cue is the source of truth rather than a PUDL model:

  mu build --plan //app
  mu build --emit-manifest //app > manifest.json
  mu observe --json //app > observe.json

Results can be ingested explicitly when needed:

  pudl mu ingest-manifest --path manifest.json --model app
  pudl mu ingest-observe --path observe.json

An ingested manifest records an apply as converging; a later real PUDL
observation is what can verify clean state.

PLUGIN OUTPUT SCHEMAS

Mu plugins may ship wire schemas plus PUDL semantic mappings. Catalog-installed
packages record this metadata in mu.lock and `mu-plugin.json`; PUDL synchronizes
it before observe ingestion. Plugin records can also use a typed envelope:

  {"schema": {...}, "definitions": [...], "data": <payload>}

See `mu guide plugins` (OUTPUT SCHEMAS) and docs/plugin-output-schemas.md.

DESIGN PRINCIPLE

Mu plugins remain ignorant of PUDL. They receive ordinary target/config data and
emit ordinary actions. PUDL coordinates desired/observed state around that
stable mu protocol.
