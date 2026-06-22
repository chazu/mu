package ewesink

import (
	"net/http"

	"github.com/chazu/ewe"
)

// Deps carries the per-action state the ewe sink functions close over. A fresh
// Deps + Registry is built per action execute (never global), so HTTP/file
// confinement and secret reveal are scoped to that action.
type Deps struct {
	Client  *http.Client      // HTTP client (nil → http.DefaultClient)
	Reveal  RevealFunc        // sealed-input resolver; nil → refs fail closed
	WorkDir string            // #ReadFile root (declared inputs)
	MuOut   string            // #WriteFile root ($MU_OUT)
	Env     map[string]string // #Env exposure (must include MU_OUT)
}

// Standard ewe function names for the populate vocabulary.
const (
	nameSecret    = "#Secret"
	nameSecretf   = "#Secretf"
	nameHTTP      = "#Http"
	nameHTTPAll   = "#HttpAll"
	nameHTTPBatch = "#HttpBatch"
	nameWriteFile = "#WriteFile"
	nameReadFile  = "#ReadFile"
	nameEnv       = "#Env"
)

// NewRegistry builds the per-execute ewe registry with the full populate
// vocabulary wired to d. #Env is given MU_OUT if not already present.
func NewRegistry(d Deps) (*ewe.Registry, error) {
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	if _, ok := d.Env["MU_OUT"]; !ok && d.MuOut != "" {
		d.Env["MU_OUT"] = d.MuOut
	}

	reg := ewe.NewRegistry()
	fns := []ewe.Function{
		SecretFunc(nameSecret),
		ewe.Secretf(nameSecretf),
		HTTPFunc(nameHTTP, d),
		HTTPAllFunc(nameHTTPAll, d),
		HTTPBatchFunc(nameHTTPBatch, d),
		WriteFileFunc(nameWriteFile, d),
		ReadFileFunc(nameReadFile, d),
		EnvFunc(nameEnv, d),
	}
	for _, fn := range fns {
		if err := reg.Register(fn); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
