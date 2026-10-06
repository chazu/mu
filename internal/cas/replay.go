package cas

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

// replayFile owns the on-disk replay buffer. Ownership passes to a Get caller,
// who releases both the descriptor and the temporary path with Close.
type replayFile struct {
	*os.File
	once     sync.Once
	closeErr error
}

func (r *replayFile) Close() error {
	r.once.Do(func() {
		r.closeErr = r.File.Close()
		if err := os.Remove(r.Name()); r.closeErr == nil {
			r.closeErr = err
		}
	})
	return r.closeErr
}

func spool(ctx context.Context, r io.Reader) (*replayFile, Digest, error) {
	f, err := os.CreateTemp("", "mu-cas-replay-*")
	if err != nil {
		return nil, Digest{}, fmt.Errorf("cas/tiered: create replay file: %w", err)
	}
	replay := &replayFile{File: f}
	digest, err := ComputeDigest(io.TeeReader(ContextReader(ctx, r), f))
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		replay.Close()
		return nil, Digest{}, fmt.Errorf("cas/tiered: spool payload: %w", err)
	}
	return replay, digest, nil
}
