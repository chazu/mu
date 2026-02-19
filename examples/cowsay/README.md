# Cowsay Example

This example uses mu to pipe a text file through `cowsay`, producing ASCII cow art.

The build target `//hello:greeting` reads `message.txt`, runs it through `cowsay`, and writes the result to `greeting.txt`.

## Prerequisites

- [Babashka (`bb`)](https://github.com/babashka/babashka) on your PATH
- `cowsay` on your PATH (install via your package manager, e.g. `brew install cowsay`)

## Running

```sh
cd examples/cowsay
../../mu build //hello:greeting
```

The output will be written to `greeting.txt`.
