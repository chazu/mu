# Spec: secrets & taint on the ewe path

Resolves ledger **S1** (folds review findings **C3**, **CRIT-3**, **MIN-1**).
Targets the **mu** sink layer (where ewe effect functions are registered) plus a
small `#Secretf` sugar function and a fail-closed guard in the **ewe** repo.

The companion engine change is [ewe-arg-resolution-spec.md](ewe-arg-resolution-spec.md)
(CRIT-1/CRIT-2); this spec assumes that has landed (nested-ref args resolve), and
*supersedes* that doc's "Follow-on: secret-as-substring" stub with a complete
design.

## The defect being fixed

ewe resolves `op.#Func & {args:…}` call sites by rewriting each result into CUE
**source text** (`goToCUEExpr` → `format.Node` → re-parse). Consequence: any
value an effect *returns* becomes literal CUE source. A `#Secret` that returned
the real token would render plaintext into an intermediate source buffer — a leak
pith never has (pith keeps the value in a `Secret{inner any}` wrapper, revealed
only at the syscall). `convert.go` also has no `pith.Secret` case.

Review #2's CRIT-3 sharpened it: even a *reference* model breaks the moment a
secret must be a **substring** (`Bearer <tok>`, `?private_token=<tok>`,
`base64(user:pass)`), because building the string in CUE re-introduces plaintext.

## The core safety property (empirically verified)

**Secrets are references in CUE; reveal happens only inside Go sinks.** The real
value never enters the CUE layer — only an inert tag does. This is safe *by
construction*, not by discipline. Verified against the real CUE engine
(2026-06-19):

| Case | Probe | Result |
|------|-------|--------|
| Interpolate a ref-struct into a string | `s: "Bearer \(x)"` where `x: {"$secret":"TOKEN"}` | **hard error**: `cannot use {$secret:"TOKEN"} (type struct) as type string` |
| Ref as a whole value | `headers: {"PRIVATE-TOKEN": x}` | concrete, inert — passes through CUE unchanged |
| Plaintext wrongly in source | `s: "Bearer ghp_real…"` | sits in source verbatim — the leak the guard must catch |

The first row is the keystone: a populator author **cannot write**
`"Bearer \(secret)"` — CUE rejects struct-into-string interpolation. There is no
syntactic path from a secret ref to a leaked string in CUE. The only residual
vector is a Go sink that wrongly returns plaintext back into CUE (row 3) — covered
by the fail-closed guard below.

## The three-layer secret model

Increasingly general; pick the least-powerful layer that fits.

### Layer 1 — whole-value reference

```cue
_tok: op.#Secret & { args: ["GITLAB_TOKEN"] }   // result: {"$secret": "GITLAB_TOKEN"}
headers: { "PRIVATE-TOKEN": _tok.result }        // header value IS the secret
```

`#Secret` returns the tag, never the value:

```go
// #Secret: [name] -> {"$secret": name}
Execute: func(_ context.Context, args []any) (any, error) {
    name, ok := args[0].(string)
    if !ok { return nil, fmt.Errorf("#Secret: name must be a string") }
    return map[string]any{"$secret": name}, nil
},
```

### Layer 2 — secret-as-substring (the general escape hatch)

A **template reference** resolves a secret *into* a larger string, in Go, at the
sink. Usable anywhere a string is needed: header value, query param, request body
field, file content, connection string.

```cue
// raw primitive (sinks understand this struct directly):
{ "$secretTemplate": "Bearer {}", refs: ["GITLAB_TOKEN"] }

// validating sugar (ewe function — builds the same struct, checks placeholder count):
_auth: op.#Secretf & { args: ["Bearer {}", "GITLAB_TOKEN"] }
//   → result: {"$secretTemplate": "Bearer {}", refs: ["GITLAB_TOKEN"]}
headers: { Authorization: _auth.result }
```

Shape:

```cue
#secretTemplate: {
    "$secretTemplate": string          // template with positional `{}` placeholders
    refs:    [...string]               // secret names, substituted in order
    encode?: "none" | "base64"         // applied to the FULL assembled string, post-reveal
}
```

`#Secretf` (ewe repo, sugar — returns a ref, never a value):

```go
// #Secretf: [template, name1, name2, …] -> {"$secretTemplate": template, refs: [...]}
Execute: func(_ context.Context, args []any) (any, error) {
    if len(args) < 1 { return nil, fmt.Errorf("#Secretf: need a template") }
    tmpl, ok := args[0].(string)
    if !ok { return nil, fmt.Errorf("#Secretf: template must be a string") }
    if n := strings.Count(tmpl, "{}"); n != len(args)-1 {
        return nil, fmt.Errorf("#Secretf: template has %d placeholders, got %d refs", n, len(args)-1)
    }
    refs := make([]any, len(args)-1)
    for i, a := range args[1:] {
        s, ok := a.(string)
        if !ok { return nil, fmt.Errorf("#Secretf: ref %d must be a string", i) }
        refs[i] = s
    }
    return map[string]any{"$secretTemplate": tmpl, "refs": refs}, nil
},
```

`#Secretf` is pure (no resolver, no I/O) and returns a struct — so it is *not* a
sink, and the result is exfil-safe by the keystone property (struct → can't be
interpolated). `encode` is set on the raw struct or by `auth.basic` (below), not
exposed as a positional `#Secretf` arg.

### Layer 3 — `auth:` block (HTTP-auth ergonomic specialization)

The `#Http`/`#HttpAll`/`#HttpBatch` request spec accepts a declarative `auth:`
block carrying **refs only**. The sink reveals + encodes + attaches in Go. Full
vocabulary:

```cue
auth: { bearer: { ref: "GITLAB_TOKEN" } }
//   → header  Authorization: Bearer <reveal(GITLAB_TOKEN)>

auth: { header: { name: "PRIVATE-TOKEN", ref: "GITLAB_TOKEN", template?: "{}" } }
//   → header  PRIVATE-TOKEN: <reveal>   (template defaults to "{}"; e.g. "token {}")

auth: { query: { param: "private_token", ref: "GITLAB_TOKEN" } }
//   → url     ?private_token=<reveal>   (URL-encoded)

auth: { basic: { userRef: "API_USER", passRef: "API_PASS" } }
//   → header  Authorization: Basic <base64(reveal(API_USER) + ":" + reveal(API_PASS))>
//     (userRef may instead be a literal `user: "string"` for non-secret usernames)
```

`auth` lowers internally to Layer-1/Layer-2 refs before resolution — it is sugar,
not a separate resolution path. Exactly one scheme per `auth` block.

```cue
#Auth: {
    bearer?: { ref: string }
    header?: { name: string, ref: string, template?: string }   // default template "{}"
    query?:  { param: string, ref: string }
    basic?:  { user?: string, userRef?: string, passRef: string }
}
```

### Out of scope: crypto/signed auth

AWS SigV4, HMAC request signing, OAuth token exchange — **not** ewe `#Http`'s job.
A secret used as a *signing key* (not a substring) needs a crypto operation over
the secret in Go that is specific to the provider. Those systems are reached
through their mu **plugin** `observe` (the `aws` plugin already does SigV4), not
raw ewe HTTP. ewe `#Http` targets simple token-auth REST. Recorded so the
"what about signing?" tail is closed, not silently unhandled.

## Sink-side resolution

`resolveSecrets` deep-walks a sink's argument immediately before the syscall,
replacing every ref with its revealed value. Its output is **never** returned to
the CUE layer.

```go
// resolveSecrets deep-walks v, replacing {"$secret":name} and
// {"$secretTemplate":tmpl,refs,encode} with revealed strings. Called ONLY inside
// sink functions, immediately before the network/file syscall.
func resolveSecrets(v any, reveal func(name string) (string, error)) (any, error) {
    switch t := v.(type) {
    case map[string]any:
        if name, ok := t["$secret"].(string); ok && len(t) == 1 {
            return reveal(name)                                  // Layer 1
        }
        if tmpl, ok := t["$secretTemplate"].(string); ok {       // Layer 2
            return expandTemplate(tmpl, t, reveal)
        }
        out := make(map[string]any, len(t))
        for k, val := range t {
            rv, err := resolveSecrets(val, reveal)
            if err != nil { return nil, err }
            out[k] = rv
        }
        return out, nil
    case []any:
        out := make([]any, len(t))
        for i, e := range t {
            rv, err := resolveSecrets(e, reveal)
            if err != nil { return nil, err }
            out[i] = rv
        }
        return out, nil
    default:
        return v, nil
    }
}
```

`expandTemplate` reveals each ref in order, substitutes `{}` left-to-right,
applies `encode`, returns the final string. `reveal` closes over mu's per-execute
sealed-input resolver. The revealed string is a transient Go value in the sink's
stack frame; the sink must not log it (same discipline pith sinks already hold).

Each HTTP sink lowers any `auth:` block to refs, then runs `resolveSecrets` over
the whole request (headers, query, body) in one pass.

## The fail-closed guard (MIN-1, corrected)

Review #2's MIN-1 is right that the *primary* safety is not a `goToCUEExpr` guard
— it is the keystone property (a ref cannot be interpolated into a string). The
guard is **defense-in-depth** against the one residual vector (a future sink
wrongly returning a revealed value into CUE — row 3 of the table):

- Define a sentinel type `type revealedSecret struct{ s string }`. `reveal` may
  optionally hand sinks a `revealedSecret` (sinks unwrap it at the syscall).
- `goToCUEExpr` gains a case: encountering a `revealedSecret` is a **hard error**,
  never a rendered literal. So if any value path ever tries to splice a revealed
  secret back into source, ewe fails loudly instead of leaking silently.

Normally never triggers — sinks consume refs and return *responses*, not secrets.
It converts "a future sink forgot the discipline" from a silent leak into a build
failure.

## No `pith.Secret` on the ewe path

The ref convention + sink-only reveal **replaces** pith's taint type for ewe.
Review #1's C3 was right that `pith.Secret` cannot be reused across the CUE
boundary (unexported field, no `MarshalJSON`); the resolution is to *sidestep*
taint-in-CUE entirely, not reimplement it. pith's `Secret`/`Reveal` stays pith's
concern (extracted to its own package only for pith's own continued life — see
ledger V3). The ewe path inherits nothing from pith and needs nothing.

## The security boundary, stated explicitly

What the model **guarantees** (taint property): a secret cannot *accidentally*
leak — into traces, logs, cache keys, error messages, or the CUE source buffer —
because plaintext never exists in the CUE layer; only refs do, and refs are
inert. Reveal is confined to registered Go sinks; populator CUE has no `resolve`
verb.

What the model **does not** guarantee: a populator that *deliberately* aims a
secret at a chosen sink (`#Http` to `evil.com` with the token in a header) will
send it. This is true of every secret system and is an **egress/sandbox** concern
— network policy on the action (`network: true` + an allowlist), not a taint
concern. Do not conflate the two: taint stops accidents; the sandbox stops
exfiltration.

## Caching interaction (defer mechanics to K1)

The action body contains only refs (names), never values — so hashing the body
for the action cache key is safe (refs in the key, values out), matching mu's
existing sealed-input rule. CRIT-3(c)'s "rotated token → stale key" is the
*intended* behavior (rotating a secret should not bust every cache) and is moot
for v1 regardless (populators are `impure` → never cached; see ledger K1). The
secret model is cache-compatible; the caching decision itself is K1.

## Authoring gotcha (bake into docs)

Do **not** `json.Marshal` a struct containing a secret ref *in CUE*. It freezes
`{"$secret":"X"}` as literal JSON text and ships the **ref** to the server (a
correctness bug — the server gets `{"$secret":"X"}`, not the token). Instead pass
structured request bodies to the sink **unmarshaled**; the sink `resolveSecrets`-
walks the body, reveals, then marshals in Go:

```cue
// WRONG: ref frozen into JSON text in CUE
body: json.Marshal({ token: _tok.result })
// RIGHT: sink reveals then marshals
bodyJSON: { token: _tok.result }            // sink walks, reveals, marshals
```

## Tests

**ewe repo (`#Secretf` + guard):**
1. `#Secretf` placeholder/ref-count mismatch → error.
2. `#Secretf` returns the template ref struct (no I/O, pure).
3. `goToCUEExpr` on a `revealedSecret` sentinel → hard error.
4. Keystone regression: `"Bearer \(x)"` with `x` a ref struct → CUE compile error
   (asserts the property cannot regress).

**mu repo (sink resolution):**
5. Layer 1: `{"$secret":"X"}` whole-value header → revealed at sink, absent from
   any trace/log capture.
6. Layer 2: `{"$secretTemplate":"Bearer {}", refs:["X"]}` → `Bearer <val>`.
7. Layer 2 multi-ref + `encode:"base64"` → `base64(a:b)`.
8. Layer 3: each `auth` scheme (bearer/header/query/basic) attaches correctly.
9. Nested ref inside a body struct resolves; pre-`json.Marshal`'d ref does **not**
   (documents the gotcha as a failing-by-design assertion + a lint note).
10. A ref that reaches a **non-sink** function stays an inert tag (fail-closed —
    never revealed off the sink path).

## Sequencing

1. **ewe repo:** `#Secretf` (pure, returns template ref) + `revealedSecret`
   sentinel + `goToCUEExpr` guard case + tests 1–4. Small; rides near the
   arg-resolution change.
2. **mu repo:** `resolveSecrets` + `expandTemplate` + per-execute `reveal` closure
   over the sealed resolver; `#Secret` (ref), and the sink functions that call
   `resolveSecrets` (`#Http`, `#HttpAll`, `#HttpBatch`, `#WriteFile`). Lower the
   `auth:` block to refs. Tests 5–10.
3. Validate against the corrected example 5 (GitLab) end to end — secret is a ref
   throughout, revealed only at the HTTP sinks.
4. Egress allowlist on `network: true` actions is tracked separately (sandbox
   concern, not this spec) — note it so the boundary is not assumed closed.
