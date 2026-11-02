package cas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
)

// Remote is the read side of a shared cache (an OCI registry in practice).
// A confirmed miss is (nil, nil) from GetActionResult and an error matching
// ErrNotFound from Get; any other error is treated as the remote being
// unreachable.
type Remote interface {
	Get(ctx context.Context, dgst Digest) (io.ReadCloser, error)
	GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error)
}

// Cache is mu's build cache: the local DiskStore plus an optional read-only
// Remote. Every write goes to Local. A read that misses locally falls back to
// Remote; whatever it returns is digest-verified and written into Local before
// it is served, so the remote is consulted at most once per entry.
//
// The remote is optional. The first remote failure is reported once through
// Warn and switches the Cache to local-only for the rest of the process; a
// build never fails because the shared cache is down.
type Cache struct {
	Local  *DiskStore
	Remote Remote // nil means local only

	// Warn reports the first remote failure. Nil logs with slog.Warn.
	Warn func(err error)

	down     atomic.Bool
	warnOnce sync.Once
}

var _ Store = (*Cache)(nil)

// Has reports whether the blob is in the local store. It never contacts the
// remote: callers that find a miss fetch through Get, which does.
func (c *Cache) Has(ctx context.Context, dgst Digest) (bool, error) {
	return c.Local.Has(ctx, dgst)
}

// Get serves the blob from the local store, fetching it from the remote into
// the local store first on a local miss. Remote bytes that do not hash to
// dgst are rejected and never stored.
func (c *Cache) Get(ctx context.Context, dgst Digest) (io.ReadCloser, error) {
	rc, err := c.Local.Get(ctx, dgst)
	if err == nil || !errors.Is(err, fs.ErrNotExist) || !c.remoteUp() {
		return rc, err
	}
	if ferr := c.fetchBlob(ctx, dgst); ferr != nil {
		if errors.Is(ferr, ErrNotFound) {
			return nil, err
		}
		return nil, ferr
	}
	return c.Local.Get(ctx, dgst)
}

// Put writes to the local store.
func (c *Cache) Put(ctx context.Context, r io.Reader) (Digest, error) {
	return c.Local.Put(ctx, r)
}

// Delete removes the blob from the local store.
func (c *Cache) Delete(ctx context.Context, dgst Digest) error {
	return c.Local.Delete(ctx, dgst)
}

// PutActionResult writes to the local store. The remote is read-only; results
// reach it only through `mu cache push`.
func (c *Cache) PutActionResult(ctx context.Context, key ActionKey, result *ActionResult) error {
	return c.Local.PutActionResult(ctx, key, result)
}

// GetActionResult serves a local result, or on a local miss a remote one. A
// remote result is used only if every output blob it names can be fetched and
// verified into the local store; the result is then recorded locally too. A
// remote result with missing or corrupt outputs is a miss, so the action
// rebuilds rather than restoring from untrusted content.
func (c *Cache) GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error) {
	result, err := c.Local.GetActionResult(ctx, key)
	if err != nil || result != nil || !c.remoteUp() {
		return result, err
	}
	result, err = c.Remote.GetActionResult(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrInvalidDigest) {
			slog.Warn("remote cache: ignoring corrupt action result", "key", key.Digest.String(), "err", err)
		} else {
			c.remoteFailed(err)
		}
		return nil, nil
	}
	if result == nil {
		return nil, nil
	}
	names := make([]string, 0, len(result.Outputs))
	for name := range result.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out := result.Outputs[name]
		if ok, herr := c.Local.Has(ctx, out); herr == nil && ok {
			continue
		}
		if ferr := c.fetchBlob(ctx, out); ferr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if c.remoteUp() {
				slog.Warn("remote cache: ignoring action result with unusable output",
					"key", key.Digest.String(), "output", name, "err", ferr)
			}
			return nil, nil
		}
	}
	if err := c.Local.PutActionResult(ctx, key, result); err != nil {
		return nil, err
	}
	slog.Debug("remote cache: restored action result", "key", key.Digest.String(), "outputs", len(names))
	return result, nil
}

// fetchBlob copies one blob from the remote into the local store, verifying
// its digest. A confirmed remote miss matches ErrNotFound and corrupt remote
// content matches ErrDigestMismatch; neither is stored. Any other remote
// failure latches the remote off (see remoteFailed) and is also returned as
// an ErrNotFound-matching miss, so callers fall back to building locally.
func (c *Cache) fetchBlob(ctx context.Context, dgst Digest) error {
	rc, err := c.Remote.Get(ctx, dgst)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidDigest) {
			return err
		}
		return c.remoteFailed(err)
	}
	src := &readTracker{r: rc}
	err = c.Local.PutExpected(ctx, dgst, src)
	rc.Close()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrDigestMismatch):
		slog.Warn("remote cache: rejected corrupt blob", "digest", dgst.String(), "err", err)
		return err
	case src.err != nil:
		return c.remoteFailed(src.err) // the remote failed mid-stream
	default:
		return err // local write failure
	}
}

// readTracker records the first non-EOF read error, separating remote stream
// failures from local write failures.
type readTracker struct {
	r   io.Reader
	err error
}

func (t *readTracker) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && err != io.EOF && t.err == nil {
		t.err = err
	}
	return n, err
}

func (c *Cache) remoteUp() bool {
	return c.Remote != nil && !c.down.Load()
}

// remoteFailed latches the remote off for the rest of the process and warns
// once. Callers must rule out caller cancellation first: that is not a remote
// failure and must leave the remote usable. The returned error matches
// ErrNotFound because an unavailable optional cache is a miss.
func (c *Cache) remoteFailed(err error) error {
	c.down.Store(true)
	c.warnOnce.Do(func() {
		if c.Warn != nil {
			c.Warn(err)
			return
		}
		slog.Warn("remote cache unavailable; continuing with the local cache only", "err", err)
	})
	return fmt.Errorf("remote cache unavailable (%v): %w", err, ErrNotFound)
}
