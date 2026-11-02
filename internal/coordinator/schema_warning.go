package coordinator

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

// schemaWarnOut receives the config_schema warnings (missing schema,
// legacy schema, unknown legacy field); tests replace it.
var schemaWarnOut io.Writer = os.Stderr

// missingSchemaWarned records the plugins already warned about, so the
// warning is printed once per plugin per process.
var missingSchemaWarned sync.Map

// warnMissingConfigSchema prints a one-time warning when a plan-capable
// plugin declares no config_schema but a target passes it config: nothing
// checks that config, so typos and wrong types reach the plugin silently.
func warnMissingConfigSchema(t config.Target, info *plugin.DiscoverResponse) {
	if info == nil || len(info.ConfigSchema) > 0 || len(t.Config) == 0 || !info.HasCapability("plan") {
		return
	}
	if _, already := missingSchemaWarned.LoadOrStore(t.Toolchain, true); already {
		return
	}
	fmt.Fprintf(schemaWarnOut,
		"warning: plugin %q declares no config_schema; config for target %q (and any other target using it) is not validated\n",
		t.Toolchain, t.Name)
}
