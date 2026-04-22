package cas

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

// TieredEvent describes a single cache operation observed by a Tiered
// store. Observers use it for structured logging and manifest telemetry.
// The zero Observer is a no-op.
type TieredEvent struct {
	Op        string // "get" | "put" | "has" | "get_action" | "put_action"
	Layer     int
	LayerName string
	Outcome   string // "hit" | "miss" | "error" | "repair"
	Digest    Digest
	Err       error
}

// LayerPolicy encodes per-layer read/write permissions. Both default to
// true when unspecified; a nil pointer from config means "unset".
type LayerPolicy struct {
	Read  bool
	Write bool
}

// DefaultLayerPolicy returns a policy that allows both read and write.
func DefaultLayerPolicy() LayerPolicy { return LayerPolicy{Read: true, Write: true} }

// Tiered composes multiple Store backends in a read-through / optional
// write-through hierarchy. Layers are consulted in slice order; layer 0 is
// the nearest (fastest) tier, higher indices are progressively slower /
// more authoritative (e.g. a shared remote registry).
//
// Reads walk the layers in order and return the first hit. With
// ReadRepair enabled, a hit at layer H > 0 back-fills writable layers
// [0, H) on the way back.
//
// Writes go to every layer whose policy permits writes when WriteThrough
// is true; otherwise only to layer 0 (the authoritative local tier).
// Errors from non-layer-0 writes are surfaced via Observer and swallowed —
// remote pushes are best-effort.
type Tiered struct {
	Layers       []Store
	LayerNames   []string // parallel to Layers; index used when missing
	Policies     []LayerPolicy
	ReadRepair   bool
	WriteThrough bool
	Observer     func(TieredEvent)
}

// Ensure Tiered satisfies the Store interface.
var _ Store = (*Tiered)(nil)

func (t *Tiered) emit(evt TieredEvent) {
	if t.Observer != nil {
		t.Observer(evt)
	}
}

func (t *Tiered) layerName(i int) string {
	if i < len(t.LayerNames) && t.LayerNames[i] != "" {
		return t.LayerNames[i]
	}
	return fmt.Sprintf("layer%d", i)
}

func (t *Tiered) policy(i int) LayerPolicy {
	if i < len(t.Policies) {
		return t.Policies[i]
	}
	return DefaultLayerPolicy()
}

// Has returns true if any read-permitted layer has the blob.
func (t *Tiered) Has(ctx context.Context, dgst Digest) (bool, error) {
	for i, l := range t.Layers {
		if !t.policy(i).Read {
			continue
		}
		ok, err := l.Has(ctx, dgst)
		if err != nil {
			t.emit(TieredEvent{Op: "has", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: dgst, Err: err})
			continue
		}
		if ok {
			t.emit(TieredEvent{Op: "has", Layer: i, LayerName: t.layerName(i), Outcome: "hit", Digest: dgst})
			return true, nil
		}
	}
	t.emit(TieredEvent{Op: "has", Outcome: "miss", Digest: dgst})
	return false, nil
}

// Get walks layers for a blob. On a hit at layer H > 0 with ReadRepair,
// the bytes are buffered in memory and written to every writable layer in
// [0, H) before being returned to the caller.
//
// The in-memory buffering is simple and bounded by the blob size. Action
// outputs are typically small-to-moderate (compiled binaries ≤ 100s of
// MB); callers streaming multi-gigabyte blobs should not use this store
// tier with ReadRepair enabled.
func (t *Tiered) Get(ctx context.Context, dgst Digest) (io.ReadCloser, error) {
	var hitLayer = -1
	var hitReader io.ReadCloser
	var firstErr error

	for i, l := range t.Layers {
		if !t.policy(i).Read {
			continue
		}
		rc, err := l.Get(ctx, dgst)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			t.emit(TieredEvent{Op: "get", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: dgst, Err: err})
			continue
		}
		// Hit.
		hitLayer = i
		hitReader = rc
		t.emit(TieredEvent{Op: "get", Layer: i, LayerName: t.layerName(i), Outcome: "hit", Digest: dgst})
		break
	}

	if hitReader == nil {
		t.emit(TieredEvent{Op: "get", Outcome: "miss", Digest: dgst})
		if firstErr != nil {
			return nil, firstErr
		}
		// All layers missed — defer to the topmost layer's Get so callers
		// see the conventional not-found error shape for the backend.
		if len(t.Layers) == 0 {
			return nil, fmt.Errorf("cas/tiered: no layers configured")
		}
		return t.Layers[0].Get(ctx, dgst)
	}

	if !t.ReadRepair || hitLayer == 0 {
		return hitReader, nil
	}

	// Buffer the hit stream and back-fill lower writable layers.
	defer hitReader.Close()
	buf, err := io.ReadAll(hitReader)
	if err != nil {
		return nil, fmt.Errorf("cas/tiered: reading hit from layer %d: %w", hitLayer, err)
	}
	for i := 0; i < hitLayer; i++ {
		if !t.policy(i).Write {
			continue
		}
		if _, perr := t.Layers[i].Put(ctx, bytes.NewReader(buf)); perr != nil {
			t.emit(TieredEvent{Op: "put", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: dgst, Err: perr})
			continue
		}
		t.emit(TieredEvent{Op: "put", Layer: i, LayerName: t.layerName(i), Outcome: "repair", Digest: dgst})
	}
	return io.NopCloser(bytes.NewReader(buf)), nil
}

// Put writes to layer 0 unconditionally. If WriteThrough is true, the
// bytes are also pushed to every other writable layer. Non-layer-0 write
// errors are emitted via Observer and swallowed (best-effort remote push).
func (t *Tiered) Put(ctx context.Context, r io.Reader) (Digest, error) {
	if len(t.Layers) == 0 {
		return Digest{}, fmt.Errorf("cas/tiered: no layers configured")
	}

	// Buffer the payload so we can hand it to multiple layers.
	buf, err := io.ReadAll(r)
	if err != nil {
		return Digest{}, fmt.Errorf("cas/tiered: buffering put payload: %w", err)
	}

	// Authoritative write: layer 0.
	dgst, err := t.Layers[0].Put(ctx, bytes.NewReader(buf))
	if err != nil {
		t.emit(TieredEvent{Op: "put", Layer: 0, LayerName: t.layerName(0), Outcome: "error", Err: err})
		return Digest{}, err
	}
	t.emit(TieredEvent{Op: "put", Layer: 0, LayerName: t.layerName(0), Outcome: "hit", Digest: dgst})

	if !t.WriteThrough {
		return dgst, nil
	}

	// Fan out to upper layers (best-effort).
	for i := 1; i < len(t.Layers); i++ {
		if !t.policy(i).Write {
			continue
		}
		if _, perr := t.Layers[i].Put(ctx, bytes.NewReader(buf)); perr != nil {
			t.emit(TieredEvent{Op: "put", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: dgst, Err: perr})
			continue
		}
		t.emit(TieredEvent{Op: "put", Layer: i, LayerName: t.layerName(i), Outcome: "hit", Digest: dgst})
	}
	return dgst, nil
}

// Delete removes from every writable layer. Errors are aggregated; the
// first non-nil error is returned after attempting all layers.
func (t *Tiered) Delete(ctx context.Context, dgst Digest) error {
	var firstErr error
	for i, l := range t.Layers {
		if !t.policy(i).Write {
			continue
		}
		if err := l.Delete(ctx, dgst); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetActionResult walks layers for a cached action result. Misses at each
// layer are `(nil, nil)` per the Store contract. On a hit at layer H > 0
// with ReadRepair, the result is replayed into writable layers [0, H);
// referenced output blobs are eagerly repaired in the same pass so that
// a subsequent Get hits the local tier too.
func (t *Tiered) GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error) {
	var hit *ActionResult
	var hitLayer = -1
	var firstErr error

	for i, l := range t.Layers {
		if !t.policy(i).Read {
			continue
		}
		r, err := l.GetActionResult(ctx, key)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			t.emit(TieredEvent{Op: "get_action", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: key.Digest, Err: err})
			continue
		}
		if r == nil {
			continue
		}
		hit = r
		hitLayer = i
		t.emit(TieredEvent{Op: "get_action", Layer: i, LayerName: t.layerName(i), Outcome: "hit", Digest: key.Digest})
		break
	}

	if hit == nil {
		t.emit(TieredEvent{Op: "get_action", Outcome: "miss", Digest: key.Digest})
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, nil
	}

	if !t.ReadRepair || hitLayer == 0 {
		return hit, nil
	}

	// Repair: copy the action result and its referenced output blobs into
	// lower writable layers. Output-blob repair is eager so the local
	// tier is fully warmed.
	for i := 0; i < hitLayer; i++ {
		if !t.policy(i).Write {
			continue
		}
		for _, out := range hit.Outputs {
			rc, err := t.Layers[hitLayer].Get(ctx, out)
			if err != nil {
				t.emit(TieredEvent{Op: "get", Layer: hitLayer, LayerName: t.layerName(hitLayer), Outcome: "error", Digest: out, Err: err})
				continue
			}
			bs, _ := io.ReadAll(rc)
			rc.Close()
			if _, perr := t.Layers[i].Put(ctx, bytes.NewReader(bs)); perr != nil {
				t.emit(TieredEvent{Op: "put", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: out, Err: perr})
			}
		}
		if perr := t.Layers[i].PutActionResult(ctx, key, hit); perr != nil {
			t.emit(TieredEvent{Op: "put_action", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: key.Digest, Err: perr})
			continue
		}
		t.emit(TieredEvent{Op: "put_action", Layer: i, LayerName: t.layerName(i), Outcome: "repair", Digest: key.Digest})
	}
	return hit, nil
}

// PutActionResult writes the result to layer 0 and, when WriteThrough,
// fans out to other writable layers (best-effort).
func (t *Tiered) PutActionResult(ctx context.Context, key ActionKey, result *ActionResult) error {
	if len(t.Layers) == 0 {
		return fmt.Errorf("cas/tiered: no layers configured")
	}
	if err := t.Layers[0].PutActionResult(ctx, key, result); err != nil {
		t.emit(TieredEvent{Op: "put_action", Layer: 0, LayerName: t.layerName(0), Outcome: "error", Digest: key.Digest, Err: err})
		return err
	}
	t.emit(TieredEvent{Op: "put_action", Layer: 0, LayerName: t.layerName(0), Outcome: "hit", Digest: key.Digest})

	if !t.WriteThrough {
		return nil
	}
	for i := 1; i < len(t.Layers); i++ {
		if !t.policy(i).Write {
			continue
		}
		if err := t.Layers[i].PutActionResult(ctx, key, result); err != nil {
			t.emit(TieredEvent{Op: "put_action", Layer: i, LayerName: t.layerName(i), Outcome: "error", Digest: key.Digest, Err: err})
			continue
		}
		t.emit(TieredEvent{Op: "put_action", Layer: i, LayerName: t.layerName(i), Outcome: "hit", Digest: key.Digest})
	}
	return nil
}
