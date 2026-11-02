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
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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

// attachReferrers attaches each spec's file to subject as an OCI referrer,
// logging progress to stderr. Shared by `mu build --publish --attach` and
// `mu plugin push --attach`. Returns the number of failed attachments;
// failures are logged and do not stop later attachments.
func attachReferrers(ctx context.Context, store *oci.OCIStore, subject ocispec.Descriptor, attachments []attachSpec, created string) int {
	failed := 0
	for _, att := range attachments {
		data, err := os.ReadFile(att.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  attach: %s: %v\n", att.Path, err)
			failed++
			continue
		}
		refDgst, err := store.AttachReferrer(ctx, subject, att.Type, filepath.Base(att.Path), data, created)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  attach: %s: %v\n", att.Path, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stderr, "    attached %s (%s) -> %s\n", filepath.Base(att.Path), att.Type, refDgst)
	}
	return failed
}

// stringSliceFlag collects a repeatable string flag into a slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// publishCandidate is one target selected for publishing. Named is true when
// the target was given literally on the command line (not only matched by a
// wildcard): a named target with nothing to publish is an error.
type publishCandidate struct {
	Target string
	Named  bool
}

// expandPublishTargets expands the CLI target patterns against the configured
// targets, in pattern order without duplicates. Dependencies pulled in by the
// build are not candidates; only what the user asked for is published.
func expandPublishTargets(patterns []string, available []string) ([]publishCandidate, error) {
	var out []publishCandidate
	index := make(map[string]int)
	for _, p := range patterns {
		names, err := coordinator.ExpandTargetPatterns([]string{p}, available)
		if err != nil {
			return nil, err
		}
		named := !coordinator.IsTargetPattern(p)
		for _, n := range names {
			if i, ok := index[n]; ok {
				out[i].Named = out[i].Named || named
				continue
			}
			index[n] = len(out)
			out = append(out, publishCandidate{Target: n, Named: named})
		}
	}
	return out, nil
}

// publishTargets pushes each requested target's outputs to the publish registry
// (config.publish) as a first-class artifact — one layer per output, a config
// blob of {target, command, created, revision, source, outputs}, tagged by git
// sha + latest. Called after a successful `mu build --publish`. Wildcard
// patterns are expanded; wildcard-matched targets without outputs are skipped,
// but an explicitly named target without outputs fails before anything is
// pushed, as does a publish that would push nothing at all. Returns an exit
// code; exitOK on success.
func publishTargets(ctx context.Context, c *cliContext, result *coordinator.BuildResult, patterns []string, attachSpecs []string) int {
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

	available := make([]string, 0, len(c.Config.Targets))
	for _, t := range c.Config.Targets {
		available = append(available, t.Name)
	}
	candidates, err := expandPublishTargets(patterns, available)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	type publishable struct {
		target   string
		outputs  map[string]cas.Digest
		command  []string
		exitCode int
	}
	var work []publishable
	var missing []string
	for _, cand := range candidates {
		outputs, command, exitCode := collectTargetOutputs(result, cand.Target)
		if len(outputs) == 0 {
			if cand.Named {
				missing = append(missing, cand.Target)
			} else {
				fmt.Fprintf(os.Stderr, "  publish: %s produced no outputs, skipping\n", cand.Target)
			}
			continue
		}
		work = append(work, publishable{cand.Target, outputs, command, exitCode})
	}
	if len(missing) > 0 {
		return c.fail(exitFail, "--publish: %s produced no outputs to publish", strings.Join(missing, ", "))
	}
	if len(work) == 0 {
		return c.fail(exitFail, "--publish: no matched target produced outputs to publish")
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
	for _, w := range work {
		target := w.target
		meta := oci.ArtifactMeta{
			Target:    target,
			Command:   w.command,
			Created:   created,
			MuVersion: muVersion,
			Revision:  revision,
			Source:    source,
			ExitCode:  w.exitCode,
			Outputs:   w.outputs,
		}

		ref := base + "/" + targetSlug(target)
		remoteRepo, err := oci.NewRepository(ref)
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
		failed += attachReferrers(ctx, store, subject, attachments, created)
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
// artifact represents).
func collectTargetOutputs(result *coordinator.BuildResult, target string) (map[string]cas.Digest, []string, int) {
	outputs := make(map[string]cas.Digest)
	var command []string
	exitCode := 0
	prefix := target + ":"
	for _, s := range result.ExecResult.Completed {
		if s.ID != target && !strings.HasPrefix(s.ID, prefix) {
			continue
		}
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
	return outputs, command, exitCode
}

// resolvePublishBase returns publish.repository ("host/namespace"), or a
// user-actionable error if it is missing.
func resolvePublishBase(c *cliContext) (string, int, bool) {
	if c.Config == nil || c.Config.Publish == nil || c.Config.Publish.Repository == "" {
		return "", c.fail(exitUsage,
			`publish is not configured in mu.cue — add:
  publish: { repository: "<host>/<namespace>" }`), false
	}
	return c.Config.Publish.Repository, exitOK, true
}

// targetSlug turns a target label into a repository path segment: "//image/
// akashic" -> "image/akashic", "//hello:greeting" -> "hello/greeting". Slashes
// are preserved (akashic accepts multi-segment repos); the leading "//" is
// stripped. The ':' separating directory from target name maps to '/' — a
// literal ':' in the ref would otherwise be parsed as a tag separator,
// silently dropping the target name from the repository path.
func targetSlug(target string) string {
	return strings.ReplaceAll(strings.TrimPrefix(target, "//"), ":", "/")
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
