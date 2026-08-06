mu guide plugin cowsay — cowsay text transform (demo plugin)

A simple example plugin that transforms text files through cowsay.
Primarily useful as a reference for writing new plugins.

USAGE IN mu.cue

  targets: [{
    target: "//fun/greeting"
    toolchain: "cowsay"
    sources: ["message.txt"]
    config: output: "greeting.txt"
  }]

CONFIG FIELDS

  output    Output file name (default: "output.txt").

EXAMPLE

  Given message.txt containing "Hello, world!", the plugin generates:

    cowsay < message.txt > greeting.txt

ACTIONS GENERATED

  cowsay    Pipes the first source file through cowsay.

CAPABILITIES

  discover, plan
