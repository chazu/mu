package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/coordinator"
)

// attachSpec is a --attach entry: an artifactType and the file to attach.
type attachSpec struct {
	Type string
	Path string
}

// parseAttachments parses repeatable "<artifactType>=<path>" flags. The '=' form
// keeps type and path paired unambiguously across multiple attachments.
func parseAttachments(specs []string) ([]attachSpec, error) {
	out := make([]attachSpec, 0, len(specs))
	for _, s := range specs {
		i := strings.IndexByte(s, '=')
		if i <= 0 || i == len(s)-1 {
			return nil, fmt.Errorf("--attach %q must be <artifactType>=<path>", s)
		}
		out = append(out, attachSpec{Type: s[:i], Path: s[i+1:]})
	}
	return out, nil
}

// stringSliceFlag collects a repeatable string flag into a slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// publishTargets pushes each requested target's outputs to the publish registry
// (config.publish) as a first-class artifact — one layer per output, a config
// blob of {target, command, created, revision, source, outputs}, tagged by git
// sha + latest. Called after a successful `mu build --publish`. Returns an exit
// code; exitOK on success.
func publishTargets(ctx context.Context, c *cliContext, result *coordinator.BuildResult, targets []string, attachSpecs []string) int {
	base, code, ok := resolvePublishBase(c)
	if !ok {
		return code
	}
	if c.Store == nil {
		return c.fail(exitFail, "--publish needs the CAS store (do not combine with --no-cache)")
	}
	attachments, err := parseAttachments(attachSpecs)
	if err != nil {
		return c.fail(exitUsage, "%v", err)
	}

	// Side-band provenance. Missing git info is non-fatal — an artifact can be
	// published from a non-repo tree; it just carries less metadata.
	revision := gitOutput("rev-parse", "HEAD")
	shortSHA := gitOutput("rev-parse", "--short", "HEAD")
	source := gitOutput("config", "--get", "remote.origin.url")
	created := time.Now().UTC().Format(time.RFC3339)

	tags := []string{"latest"}
	if shortSHA != "" {
		tags = []string{shortSHA, "latest"}
	}

	var published, failed int
	for _, target := range targets {
		outputs, command, exitCode, found := collectTargetOutputs(result, target)
		if !found || len(outputs) == 0 {
			fmt.Fprintf(os.Stderr, "  publish: %s produced no outputs, skipping\n", target)
			continue
		}

		meta := oci.ArtifactMeta{
			Target:    target,
			Command:   command,
			Created:   created,
			MuVersion: muVersion,
			Revision:  revision,
			Source:    source,
			ExitCode:  exitCode,
			Outputs:   outputs,
		}

		ref := base + "/" + targetSlug(target)
		remoteRepo, err := newPushRepository(ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  publish: %s: %v\n", target, err)
			failed++
			continue
		}
		store := oci.New(remoteRepo)
		subject, err := store.PublishArtifact(ctx, meta, c.Store, tags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  publish: %s: %v\n", target, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stderr, "  published %s -> %s:%s (%s)\n", target, ref, tags[0], subject.Digest)
		published++

		// Attach referrers (SBOMs, provenance, logs) to this artifact.
		for _, att := range attachments {
			data, readErr := os.ReadFile(att.Path)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "  attach: %s: %v\n", att.Path, readErr)
				failed++
				continue
			}
			refDgst, attErr := store.AttachReferrer(ctx, subject, att.Type, filepath.Base(att.Path), data, created)
			if attErr != nil {
				fmt.Fprintf(os.Stderr, "  attach: %s: %v\n", att.Path, attErr)
				failed++
				continue
			}
			fmt.Fprintf(os.Stderr, "    attached %s (%s) -> %s\n", filepath.Base(att.Path), att.Type, refDgst)
		}
	}

	fmt.Fprintf(os.Stderr, "  published %d target(s)", published)
	if failed > 0 {
		fmt.Fprintf(os.Stderr, ", %d failed", failed)
	}
	fmt.Fprintln(os.Stderr)
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

// collectTargetOutputs merges the outputs of every completed action belonging to
// a target: the action named exactly for the target, plus per-output actions
// "<target>:<name>" (e.g. "//cmd/mu:build"). Dependency actions (different IDs)
// are excluded. It returns the merged outputs plus the command and exit code of
// the output-producing action (the target's terminal action, whose argv the
// artifact represents), and whether any action matched.
func collectTargetOutputs(result *coordinator.BuildResult, target string) (map[string]cas.Digest, []string, int, bool) {
	outputs := make(map[string]cas.Digest)
	var command []string
	exitCode, found := 0, false
	prefix := target + ":"
	for _, s := range result.ExecResult.Completed {
		if s.ID != target && !strings.HasPrefix(s.ID, prefix) {
			continue
		}
		found = true
		for name, dgst := range s.Outputs {
			outputs[name] = dgst
		}
		// The action that actually emitted outputs carries the command + exit
		// code the published artifact should record.
		if len(s.Outputs) > 0 {
			exitCode = s.ExitCode
			if a := result.Graph.Action(s.ID); a != nil {
				command = a.Command
			}
		}
	}
	return outputs, command, exitCode, found
}

// resolvePublishBase returns "<registry>/<repository>" from config.publish, or a
// user-actionable error naming the missing field.
func resolvePublishBase(c *cliContext) (string, int, bool) {
	if c.Config == nil || c.Config.Publish == nil {
		return "", c.fail(exitUsage,
			`publish is not configured in mu.cue — add:
  publish: { registry: "<host>", repository: "<org>" }`), false
	}
	p := c.Config.Publish
	if p.Registry == "" {
		return "", c.fail(exitUsage, `publish.registry is not set in mu.cue`), false
	}
	if p.Repository == "" {
		return "", c.fail(exitUsage, `publish.repository is not set in mu.cue`), false
	}
	return p.Registry + "/" + p.Repository, exitOK, true
}

// targetSlug turns a target label into a repository path segment: "//image/
// akashic" -> "image/akashic". Slashes are preserved (akashic accepts
// multi-segment repos); the leading "//" is stripped.
func targetSlug(target string) string {
	return strings.TrimPrefix(target, "//")
}

// gitOutput runs `git <args>` at the cwd and returns trimmed stdout, or "" on
// any error (missing git, not a repo, etc.) — publish provenance is best-effort.
func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
