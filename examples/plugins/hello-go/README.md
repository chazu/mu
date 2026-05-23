# hello-go

Canonical 30-line example of a mu plugin written in Go against
`sdk/muplugin`.

## Build

```bash
go build -o hello-go .
```

## Run standalone (talk NDJSON by hand)

```bash
echo '{"method":"discover"}' | ./hello-go
echo '{"method":"plan","target":{"name":"//hello","toolchain":"hello","config":{"out":"hello.txt","message":"hi"}}}' | ./hello-go
```

## Use from mu

Reference the binary from a `mu.cue`:

```cue
package mu

plugins: [{name: "hello", command: ["./examples/plugins/hello-go/hello-go"]}]

targets: [{
    target:    "//hello"
    toolchain: "hello"
    sources: []
    config: {
        out:     "hello.txt"
        message: "hello from mu"
    }
}]
```

```bash
mu build //hello
cat hello.txt
```

## What's interesting here

- One struct literal + one `Main()` call = a complete plugin.
- No NDJSON loop, no JSON encode/decode, no capabilities array — the
  SDK handles those. Capabilities are auto-derived from which optional
  handlers (`Observe`, `ResolveSecret`, `StoreSecret`, `Advise`) are
  non-nil on the `Plugin` struct.
- The plugin can be unit-tested without spawning a subprocess via
  `muplugin.Exchange` / `muplugin.ExchangeInto`.

Full SDK reference: `mu guide sdk` or `docs/guide/sdk.md`.
