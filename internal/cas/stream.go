package cas

import (
	"context"
	"io"
)

// ContextReader checks cancellation between reads. The underlying reader must
// itself support interruption to cancel a read already blocked in I/O.
func ContextReader(ctx context.Context, r io.Reader) io.Reader {
	return contextReader{ctx: ctx, reader: r}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
