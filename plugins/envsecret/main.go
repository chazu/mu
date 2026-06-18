// envsecret: a minimal read-only secret provider that resolves a ref to the
// value of an environment variable. Scheme "env": a ref of "env:NAME" returns
// $NAME from the plugin process environment.
//
// This exists so a sealed_input can be fed from the ambient environment for
// local/demo use without a keyring (pass/sops). It is intentionally read-only:
// Store is unimplemented, so it advertises only resolve_secret.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/chazu/mu/sdk/muplugin"
)

type envBackend struct{}

// Resolve returns $ref. The coordinator strips the "env:" scheme and passes the
// remainder (the var name) as ref.
func (envBackend) Resolve(_ context.Context, ref string) (string, error) {
	val, ok := os.LookupEnv(ref)
	if !ok {
		return "", fmt.Errorf("envsecret: environment variable %q is not set", ref)
	}
	return val, nil
}

// Store is a no-op error: this provider is read-only. Returning an error keeps
// store_secret out of the advertised capabilities path for any write attempt.
func (envBackend) Store(_ context.Context, ref, _, _ string) error {
	return fmt.Errorf("envsecret: read-only provider cannot store %q", ref)
}

func main() {
	muplugin.SecretPlugin("env", "0.1.0", envBackend{}).Main()
}
