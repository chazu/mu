# Bootstrap Example

This example uses mu's bootstrap plugin to download, verify, and install a CLI tool. It fetches a specific version of `jq` (1.7.1), verifies its SHA-256 checksum, and places the binary under `toolchains/jq/1.7.1/bin/`.

## Prerequisites

- [Babashka (`bb`)](https://github.com/babashka/babashka) on your PATH

## Running

```sh
cd examples/bootstrap
../../mu build //tools:jq
```

After a successful build, the `jq` binary will be available at `toolchains/jq/1.7.1/bin/jq`.
