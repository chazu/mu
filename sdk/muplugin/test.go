package muplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// Exchange runs a single request through the plugin's dispatch loop
// in-process (no subprocess) and returns the decoded response. It is the
// recommended way to unit-test a Plugin from Go test code:
//
//	resp, err := muplugin.Exchange(t.Context(), p, muplugin.NewDiscoverRequest())
//	if err != nil { t.Fatal(err) }
//	got := resp.(map[string]any)
//
// The returned value is decoded into a generic map[string]any so tests
// can assert on the wire shape without coupling to internal Go types.
// Use ExchangeInto for typed decoding.
func Exchange(ctx context.Context, p *Plugin, req Request) (map[string]any, error) {
	var out map[string]any
	if err := exchange(ctx, p, req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ExchangeInto is the typed variant of Exchange: it decodes the response
// into the value pointed to by dst.
func ExchangeInto(ctx context.Context, p *Plugin, req Request, dst any) error {
	return exchange(ctx, p, req, dst)
}

func exchange(ctx context.Context, p *Plugin, req Request, dst any) error {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("muplugin.Exchange: marshal request: %w", err)
	}
	in := bytes.NewReader(append(reqBytes, '\n'))
	var out bytes.Buffer
	if err := p.Run(ctx, in, &out); err != nil {
		return fmt.Errorf("muplugin.Exchange: run: %w", err)
	}
	if out.Len() == 0 {
		return fmt.Errorf("muplugin.Exchange: plugin produced no response")
	}
	return json.Unmarshal(out.Bytes(), dst)
}
