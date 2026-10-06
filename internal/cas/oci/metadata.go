package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/chazu/mu/internal/cas"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Metadata budgets are independent of potentially multi-gigabyte layer blobs.
// Eight MiB accommodates manifests/results with tens of thousands of ordinary
// named outputs; plugin descriptors and indexes have smaller four MiB budgets.
const (
	MaxManifestBytes     int64 = 8 << 20
	MaxActionConfigBytes int64 = 8 << 20
	MaxPluginConfigBytes int64 = 4 << 20
	MaxPluginIndexBytes  int64 = 4 << 20
)

var ErrMetadataTooLarge = errors.New("OCI metadata exceeds size limit")

// decodeMetadata bounds actual bytes, including trailing whitespace, before
// JSON decoding. LimitReader alone followed by Decoder.Decode would accept an
// early JSON object and never discover an oversized tail.
func decodeMetadata(ctx context.Context, r io.Reader, limit int64, dest any) error {
	data, err := io.ReadAll(io.LimitReader(cas.ContextReader(ctx, r), limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%w: limit %d bytes", ErrMetadataTooLarge, limit)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func fetchMetadata(ctx context.Context, repo Registry, desc ocispec.Descriptor, kind string, limit int64, dest any) error {
	if desc.Size > limit {
		return fmt.Errorf("%s: %w: declared %d bytes, limit %d bytes", kind, ErrMetadataTooLarge, desc.Size, limit)
	}
	if desc.Size < 0 {
		return fmt.Errorf("%s: invalid declared size %d", kind, desc.Size)
	}
	rc, err := repo.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", kind, err)
	}
	decodeErr := decodeMetadata(ctx, rc, limit, dest)
	closeErr := rc.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode %s: %w", kind, decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", kind, closeErr)
	}
	return nil
}
