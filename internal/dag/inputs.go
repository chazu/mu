package dag

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/chazu/mu/internal/cas"
)

// InputSource records which planned action produces an input. At plan time
// the input's digest is a zero placeholder because the producer has not run;
// the executor replaces it with the real digest once the producer completes
// and before the consumer's action key is computed.
type InputSource struct {
	// ProducerID is the ID of the action that produces the input.
	ProducerID string
	// OutputPath is the producer's declared output path. Empty means the
	// input stands for all of the producer's declared outputs ("{action:id}").
	OutputPath string
}

// completedAction is what the executor remembers about a finished action so
// that downstream placeholder inputs can be resolved.
type completedAction struct {
	action  *Action
	outputs map[string]cas.Digest // declared output path -> digest, when recorded
}

// PendingInputs returns the sorted names of inputs whose digests are not known
// until an upstream action has run.
func (a *Action) PendingInputs() []string {
	return sortedKeys(a.InputSources)
}

// resolveUpstreamInputs fills every placeholder input of a with the digest of
// the content its producer actually produced. It must run after all of a's
// dependencies have completed and before ComputeActionKey(a).
func (e *Executor) resolveUpstreamInputs(a *Action) error {
	if len(a.InputSources) == 0 {
		return nil
	}
	e.outputsMu.RLock()
	defer e.outputsMu.RUnlock()
	for _, name := range sortedKeys(a.InputSources) {
		src := a.InputSources[name]
		producer, ok := e.completed[src.ProducerID]
		if !ok {
			return fmt.Errorf("input %q: producer %q has not completed", name, src.ProducerID)
		}
		var digest cas.Digest
		var err error
		if src.OutputPath == "" {
			digest, err = producer.allOutputsDigest()
		} else {
			digest, err = producer.outputDigest(src.OutputPath)
		}
		if err != nil {
			return fmt.Errorf("input %q from %q: %w", name, src.ProducerID, err)
		}
		if a.Inputs == nil {
			a.Inputs = make(map[string]cas.Digest, len(a.InputSources))
		}
		a.Inputs[name] = digest
	}
	return nil
}

// outputDigest returns the recorded digest of one output. Impure producers (or
// executors without a store) record none, so the file is hashed in place.
func (c completedAction) outputDigest(path string) (cas.Digest, error) {
	if d, ok := c.outputs[path]; ok && !d.IsZero() {
		return d, nil
	}
	f, closeFn, err := openActionOutput(c.action, path)
	if err != nil {
		return cas.Digest{}, err
	}
	defer closeFn()
	return cas.ComputeDigest(f)
}

// allOutputsDigest identifies the whole declared output set of the producer:
// a hash over its sorted (path, digest) pairs.
func (c completedAction) allOutputsDigest() (cas.Digest, error) {
	h := sha256.New()
	field := func(s string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	field("mu.action-outputs/v1")
	paths := append([]string(nil), c.action.Outputs...)
	sort.Strings(paths)
	for _, path := range paths {
		d, err := c.outputDigest(path)
		if err != nil {
			return cas.Digest{}, err
		}
		field(path)
		field(d.String())
	}
	return cas.NewSHA256(hex.EncodeToString(h.Sum(nil))), nil
}

// openActionOutput opens a declared output of a, resolving relative paths
// against its work directory (confined to the project root when one is set).
func openActionOutput(a *Action, path string) (*os.File, func(), error) {
	if a.ProjectRoot != "" && !filepath.IsAbs(path) {
		root, err := OpenWorkRoot(a.ProjectRoot, a.WorkDir)
		if err != nil {
			return nil, nil, err
		}
		f, err := root.Open(path)
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		return f, func() { f.Close(); root.Close() }, nil
	}
	if !filepath.IsAbs(path) && a.WorkDir != "" {
		path = filepath.Join(a.WorkDir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}
