// Package guide embeds mu guide markdown content so the mu binary
// can render it without depending on a filesystem layout.
package guide

import (
	"embed"
	"strings"
)

//go:embed *.md
var files embed.FS

// topicFiles maps mu guide topic keys to their embedded filename.
// Some topic keys contain characters that aren't filename-friendly
// (e.g. "mu.cue") so the mapping is explicit.
var topicFiles = map[string]string{
	"index":            "index.md",
	"overview":         "overview.md",
	"mu.cue":           "mu-cue.md",
	"build":            "build.md",
	"plugins":          "plugins.md",
	"observe":          "observe.md",
	"pudl":             "pudl.md",
	"cache":            "cache.md",
	"secrets":          "secrets.md",
	"secret-gen":       "secret-gen.md",
	"toolchains":       "toolchains.md",
	"shell":            "shell.md",
	"secret-providers": "secret-providers.md",
	"protocol":         "protocol.md",
	"pith-plugins":     "pith-plugins.md",
	"sandbox":          "sandbox.md",
	"advice":           "advice.md",
	"sdk":              "sdk.md",
}

// Get returns the markdown body for the given topic key. The returned
// string ends with a single trailing newline so callers can fmt.Print
// it directly and match the previous fmt.Print(`…`) behaviour.
func Get(topic string) (string, bool) {
	name, ok := topicFiles[topic]
	if !ok {
		return "", false
	}
	data, err := files.ReadFile(name)
	if err != nil {
		return "", false
	}
	s := strings.TrimRight(string(data), "\n") + "\n"
	return s, true
}
