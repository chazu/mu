package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/registryhttp"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// runCachePush publishes every cached action result (and its output blobs)
// from the local disk cache at ~/.mu/cache to the remote OCI registry
// configured under cache.push in mu.cue. Each per-key local receipt becomes an
// OCI manifest tagged action-sha256-<key> on the remote, built at push time.
// Blobs already present remotely are not uploaded again.
func runCachePush(args []string) int {
	fs := flag.NewFlagSet("cache push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache push", fs, useConfig, useVerbose, useOffline)
	dryRun := fs.Bool("dry-run", false, "list action tags that would be pushed, without pushing")
	if code, ok := parseFlags(fs, args, "cache push [flags]"); !ok {
		return code
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}

	cacheDir := c.CachePath()
	local, err := openLocalCache(cacheDir)
	if err != nil {
		return c.fail(exitFail, "opening local cache %s: %v", cacheDir, err)
	}
	actions, err := readCachedActions(local, false)
	if err != nil {
		return c.fail(exitFail, "reading local cache: %v", err)
	}
	tags := make([]string, 0, len(actions))
	for _, a := range actions {
		tags = append(tags, a.Tag)
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

	if !c.requireRegistry() {
		return exitUsage
	}

	ctx := c.networkContext(context.Background())

	remoteRepo, err := newPushRepository(ref)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	remote := oci.New(remoteRepo)

	var pushed, failed int
	for _, a := range actions {
		if err := remote.PushActionResult(ctx, local, a.Key, a.Result); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", a.Tag, err)
			failed++
			break
		}
		if c.Verbose {
			fmt.Printf("  pushed %s\n", a.Tag)
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

	httpClient := registryhttp.NewClient(registryhttp.Options{})
	client := &auth.Client{Cache: auth.NewCache(), Client: httpClient}
	if credStore, err := openCredentialStore(); err == nil {
		client.Credential = credentials.Credential(credStore)
	}
	if os.Getenv("MU_HTTP_TRACE") != "" {
		httpClient.Transport = &tracingTransport{inner: httpClient.Transport}
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
	if dump, err := httputil.DumpRequestOut(redactedRequest(req), false); err == nil {
		fmt.Fprintf(os.Stderr, "--> %s\n", dump)
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if dump, derr := httputil.DumpResponse(redactedResponse(resp), false); derr == nil {
		fmt.Fprintf(os.Stderr, "<-- %s\n", dump)
	}
	return resp, err
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

func redactedHeaders(header http.Header) http.Header {
	copy := header.Clone()
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"} {
		if copy.Get(name) != "" {
			copy.Set(name, "[redacted]")
		}
	}
	return copy
}
func redactedRequest(req *http.Request) *http.Request {
	copy := req.Clone(req.Context())
	copy.Header = redactedHeaders(req.Header)
	return copy
}
func redactedResponse(resp *http.Response) *http.Response {
	copy := *resp
	copy.Header = redactedHeaders(resp.Header)
	return &copy
}
