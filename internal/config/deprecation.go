package config

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// deprecationMessage is the one-line warning emitted (at most once per
// process) when a mu.json file is loaded. It is exported as a constant
// for tests and downstream tooling that may want to grep for it.
const deprecationMessage = "mu.json is deprecated; run mu migrate to convert to mu.cue"

// suppressDeprecationEnv is the environment variable that, when set to
// "1", silences the mu.json deprecation warning.
const suppressDeprecationEnv = "MU_SUPPRESS_DEPRECATION"

// deprecationWriter is the io.Writer the deprecation warning is written
// to. It defaults to os.Stderr and is overridable by tests.
var deprecationWriter io.Writer = os.Stderr

// deprecationOnce ensures the warning is emitted at most once per
// process. Tests reset it via resetDeprecationOnce.
var deprecationOnce sync.Once

// warnJSONDeprecated prints the mu.json deprecation warning to
// deprecationWriter, exactly once per process. If the environment
// variable MU_SUPPRESS_DEPRECATION is set to "1", the warning is
// suppressed (but the once is still consumed, so toggling the env var
// mid-process does not re-enable the warning).
func warnJSONDeprecated() {
	deprecationOnce.Do(func() {
		if os.Getenv(suppressDeprecationEnv) == "1" {
			return
		}
		fmt.Fprintln(deprecationWriter, deprecationMessage)
	})
}

// resetDeprecationOnce resets the dedupe latch. Test-only helper.
func resetDeprecationOnce() {
	deprecationOnce = sync.Once{}
}
