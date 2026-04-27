package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	ocilayout "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// runCachePush copies every cached action manifest (and all its referenced
// blobs) from the local OCI layout at ~/.mu/cache to the remote OCI registry
// configured under cache.push in mu.cue. Blobs already present remotely are
// skipped by oras.Copy.
func runCachePush(args []string) int {
	fs := flag.NewFlagSet("cache push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache push", fs)
	dryRun := fs.Bool("dry-run", false, "list action tags that would be pushed, without pushing")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}

	cacheDir := c.CachePath()
	entries, err := readIndex(cacheDir)
	if err != nil {
		return c.fail(exitFail, "reading local cache index: %v", err)
	}

	var tags []string
	for _, e := range entries {
		if strings.HasPrefix(e.Tag, "action-") {
			tags = append(tags, e.Tag)
		}
	}

	if len(tags) == 0 {
		fmt.Println("No cached actions to push.")
		return exitOK
	}

	if *dryRun {
		fmt.Printf("Would push %d action(s) to %s:\n", len(tags), ref)
		for _, t := range tags {
			fmt.Printf("  %s\n", t)
		}
		return exitOK
	}

	ctx := context.Background()

	local, err := ocilayout.New(cacheDir)
	if err != nil {
		return c.fail(exitFail, "opening local cache %s: %v", cacheDir, err)
	}

	remoteRepo, err := newPushRepository(ref)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	copyOpts := oras.DefaultCopyOptions
	copyOpts.FindSuccessors = sizeFillingSuccessors(cacheDir)

	var pushed, failed int
	for _, tag := range tags {
		if _, err := oras.Copy(ctx, local, tag, remoteRepo, tag, copyOpts); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", tag, err)
			failed++
			continue
		}
		if c.Verbose {
			fmt.Printf("  pushed %s\n", tag)
		}
		pushed++
	}

	fmt.Printf("Pushed %d/%d action(s) to %s", pushed, len(tags), ref)
	if failed > 0 {
		fmt.Printf(" (%d failed)", failed)
	}
	fmt.Println()
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

// resolvePushRef returns the fully-qualified "<registry>/<repository>"
// reference from the loaded config, or a user-actionable error explaining
// which field is missing.
func resolvePushRef(c *cliContext) (string, int, bool) {
	if c.Config == nil || c.Config.Cache == nil || c.Config.Cache.Push == nil {
		return "", c.fail(exitUsage,
			`cache.push is not configured in mu.cue — add:
  "cache": { "push": { "registry": "<host>", "repository": "<repo>" } }`), false
	}
	p := c.Config.Cache.Push
	if p.Registry == "" {
		return "", c.fail(exitUsage,
			`cache.push.registry is not set in mu.cue — set it to the OCI registry host (e.g. "registry.loosh.cloud")`), false
	}
	if p.Repository == "" {
		return "", c.fail(exitUsage,
			`cache.push.repository is not set in mu.cue — add it under cache.push (e.g. "repository": "mu-cache")`), false
	}
	return p.Registry + "/" + p.Repository, exitOK, true
}

func newPushRepository(ref string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("parse registry reference %q: %w", ref, err)
	}
	if isLocalRegistryHost(ref) {
		repo.PlainHTTP = true
	}

	client := &auth.Client{Cache: auth.NewCache()}
	if credStore, err := openCredentialStore(); err == nil {
		client.Credential = credentials.Credential(credStore)
	}
	if os.Getenv("MU_HTTP_TRACE") != "" {
		client.Client = &http.Client{Transport: &tracingTransport{inner: http.DefaultTransport}}
	}
	repo.Client = client
	return repo, nil
}

// tracingTransport dumps each request/response to stderr when MU_HTTP_TRACE
// is set. Intended for debugging only.
type tracingTransport struct {
	inner http.RoundTripper
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dump, err := httputil.DumpRequestOut(req, false); err == nil {
		fmt.Fprintf(os.Stderr, "--> %s\n", dump)
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if dump, derr := httputil.DumpResponse(resp, true); derr == nil {
		fmt.Fprintf(os.Stderr, "<-- %s\n", dump)
	}
	return resp, err
}

// sizeFillingSuccessors returns a FindSuccessors hook that runs the default
// successor walk and then fills in any descriptors whose Size is zero by
// stat'ing the corresponding blob file in the local OCI layout. This is
// needed because mu cache manifests written before the CAS writer started
// recording layer sizes have Size=0 on layer descriptors. oras-go propagates
// that zero into req.ContentLength on the blob PUT, which makes Go's http
// client fall back to Transfer-Encoding: chunked — which strict registries
// (Zot) reject with 400 on the monolithic-upload PUT.
func sizeFillingSuccessors(cacheDir string) func(context.Context, content.Fetcher, ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	blobsDir := filepath.Join(cacheDir, "blobs")
	return func(ctx context.Context, fetcher content.Fetcher, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		succs, err := content.Successors(ctx, fetcher, desc)
		if err != nil {
			return nil, err
		}
		for i := range succs {
			if succs[i].Size > 0 {
				continue
			}
			d := succs[i].Digest
			p := filepath.Join(blobsDir, d.Algorithm().String(), d.Encoded())
			info, statErr := os.Stat(p)
			if statErr != nil {
				continue
			}
			succs[i].Size = info.Size()
		}
		return succs, nil
	}
}

// isLocalRegistryHost mirrors internal/cas/oci.isLocalRegistry for the push
// path: localhost / 127.0.0.1 registries are served over plain HTTP.
func isLocalRegistryHost(ref string) bool {
	host := ref
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		host = ref[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}
