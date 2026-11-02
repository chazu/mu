package dag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/sandbox"
)

// SandboxEnvVar opts every pure action into the sandbox when set to "1".
// Until plugins stop depending on host-installed tools (mu-oa1.3, mu-oa1.4)
// the sandbox is off by default; actions that name scratch toolchains are
// sandboxed regardless, since their tools exist only inside the sandbox.
const SandboxEnvVar = "MU_SANDBOX"

// SandboxEnabled reports whether MU_SANDBOX=1 is set in mu's environment.
func SandboxEnabled() bool { return os.Getenv(SandboxEnvVar) == "1" }

// Sandboxed reports whether the executor runs a in the sandbox. Impure
// actions (side effects on the host) always run on the host. Pure actions run
// in the sandbox when MU_SANDBOX=1 or when they name scratch toolchains.
func (a *Action) Sandboxed() bool {
	return !a.Impure && (a.Toolchain != nil || SandboxEnabled())
}

// projectBase is the directory the sandbox's work tree mirrors: the project
// root, or the work directory for direct library actions without one.
func projectBase(a *Action) string {
	if a.ProjectRoot != "" {
		return a.ProjectRoot
	}
	if a.WorkDir != "" {
		return a.WorkDir
	}
	return "."
}

// workSubdir is a's work directory relative to projectBase(a).
func workSubdir(a *Action) (string, error) {
	if a.ProjectRoot == "" || a.WorkDir == "" {
		return ".", nil
	}
	root, err := filepath.Abs(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	work, err := filepath.Abs(a.WorkDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, work)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return "", fmt.Errorf("work_dir %q escapes project root", a.WorkDir)
	}
	return rel, nil
}

// executeInSandbox runs one attempt of a in a fresh sandbox. The sandbox root
// holds the unpacked toolchain artifacts; work/ mirrors the project root and
// holds exactly the action's file inputs (project files and upstream outputs,
// each verified against the digest in the action key). The command runs in
// work/<work_dir>, and its declared outputs are copied back to the host work
// directory. sealedFiles are file-mode sealed inputs, delivered inside the
// sandbox and removed as soon as the command exits.
func (e *Executor) executeInSandbox(ctx context.Context, a *Action, env, sealedFiles map[string]string, workRoot *os.Root) (int, error) {
	for _, out := range a.Outputs {
		if !filepath.IsLocal(out) {
			return -1, fmt.Errorf("sandboxed action output %q escapes its work directory", out)
		}
	}
	if len(a.Toolchain) > 0 && e.Store == nil {
		return -1, fmt.Errorf("sandboxed action names toolchains but the executor has no CAS store")
	}
	sub, err := workSubdir(a)
	if err != nil {
		return -1, err
	}

	sb, err := sandbox.New(e.Store)
	if err != nil {
		return -1, fmt.Errorf("create sandbox: %w", err)
	}
	defer sb.Cleanup()
	sb.Stdout = e.SubprocessStdout

	if err := sb.UnpackToolchain(ctx, a.Toolchain); err != nil {
		return -1, fmt.Errorf("unpack toolchain: %w", err)
	}
	if err := e.materializeInputs(ctx, sb, a); err != nil {
		return -1, err
	}
	if err := sb.SetWorkSubdir(sub); err != nil {
		return -1, err
	}

	env = maps.Clone(env)
	if env == nil {
		env = make(map[string]string)
	}
	// The sandbox prepends its bin/ to PATH; without a declared PATH the rest
	// is the same fixed system path bare actions get.
	if _, ok := env["PATH"]; !ok {
		env["PATH"] = DefaultActionPath
	}
	if hasRelativeOutputs(a.Outputs) {
		env["MU_OUT"] = sb.VisiblePath("out")
	}
	paths, err := sb.WriteSealedFiles(sealedFiles)
	if err != nil {
		return -1, err
	}
	maps.Copy(env, paths)

	exitCode, err := sb.Exec(ctx, a.Command, env, a.Network)
	if rmErr := sb.RemoveSealedFiles(); rmErr != nil && err == nil {
		err = fmt.Errorf("remove sealed inputs: %w", rmErr)
	}
	if err != nil {
		return exitCode, err
	}

	// Copy declared outputs back to the host work directory. Like bare
	// actions, an output may be written to $MU_OUT or to the working directory.
	for _, outRel := range a.Outputs {
		src, err := sb.OpenOutput(outRel)
		if err != nil {
			return exitCode, fmt.Errorf("open sandbox output %s: %w", outRel, err)
		}
		hostOut := outRel
		if workRoot == nil {
			hostOut = filepath.Join(a.WorkDir, outRel)
		}
		err = writeFile(ctx, hostOut, src, workRoot)
		src.Close()
		if err != nil {
			return exitCode, fmt.Errorf("copy output %s: %w", outRel, err)
		}
	}
	return exitCode, nil
}

// sandboxInput is one file to place in the sandbox's work tree.
type sandboxInput struct {
	name     string     // input name, for errors
	path     string     // project-relative path
	digest   cas.Digest // content identity hashed into the action key
	upstream bool       // produced by another action: fetch from CAS first
}

// sandboxInputs lists every file a's inputs place in the work tree.
func (e *Executor) sandboxInputs(a *Action) ([]sandboxInput, error) {
	var files []sandboxInput
	e.outputsMu.RLock()
	defer e.outputsMu.RUnlock()
	for _, name := range sortedKeys(a.Inputs) {
		src, upstream := a.InputSources[name]
		if upstream && src.OutputPath == "" {
			// "{action:id}" stands for all of the producer's outputs; place
			// each relative one at its project path. Absolute outputs live on
			// the host and have no place in the work tree.
			producer, ok := e.completed[src.ProducerID]
			if !ok {
				return nil, fmt.Errorf("input %q: producer %q has not completed", name, src.ProducerID)
			}
			producerSub, err := workSubdir(producer.action)
			if err != nil {
				return nil, fmt.Errorf("input %q: producer %q: %w", name, src.ProducerID, err)
			}
			for _, out := range producer.action.Outputs {
				if filepath.IsAbs(out) {
					continue
				}
				d, err := producer.outputDigest(out)
				if err != nil {
					return nil, fmt.Errorf("input %q from %q: %w", name, src.ProducerID, err)
				}
				files = append(files, sandboxInput{name, filepath.Join(producerSub, out), d, true})
			}
			continue
		}
		path, ok := a.InputPaths[name]
		if !ok && upstream {
			path = src.OutputPath
		}
		if !ok && !upstream {
			return nil, fmt.Errorf("input %q has no project path and cannot be placed in the sandbox", name)
		}
		files = append(files, sandboxInput{name, filepath.Clean(filepath.FromSlash(path)), a.Inputs[name], upstream})
	}
	return files, nil
}

// materializeInputs places every file input of a into the sandbox's work tree
// at its project-relative path. Upstream outputs are fetched from CAS by
// digest, falling back to the copy the producer left in the project; project
// files are read through an os.Root pinned at the project root. Every file's
// bytes are checked against the digest in the action key, so the sandbox
// sees exactly what the cache key describes.
func (e *Executor) materializeInputs(ctx context.Context, sb *sandbox.Sandbox, a *Action) error {
	files, err := e.sandboxInputs(a)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	project, err := os.OpenRoot(projectBase(a))
	if err != nil {
		return fmt.Errorf("open project root: %w", err)
	}
	defer project.Close()

	placed := make(map[string]cas.Digest, len(files))
	for _, in := range files {
		if !filepath.IsLocal(in.path) {
			return fmt.Errorf("input %q: path %q is not inside the project", in.name, in.path)
		}
		if prev, ok := placed[in.path]; ok {
			if prev == in.digest {
				continue
			}
			return fmt.Errorf("input %q: %s is also another input with different content", in.name, in.path)
		}
		if err := e.placeInput(ctx, sb, project, in); err != nil {
			return fmt.Errorf("input %q (%s): %w", in.name, in.path, err)
		}
		placed[in.path] = in.digest
	}
	return nil
}

func (e *Executor) placeInput(ctx context.Context, sb *sandbox.Sandbox, project *os.Root, in sandboxInput) error {
	// Permission bits come from the project copy when there is one: CAS
	// blobs carry no mode, and an upstream binary must stay executable.
	perm := os.FileMode(0o644)
	if info, err := project.Stat(in.path); err == nil && info.Mode().IsRegular() {
		perm = info.Mode().Perm()
	}
	var r io.ReadCloser
	if in.upstream && e.Store != nil {
		if rc, err := e.Store.Get(ctx, in.digest); err == nil {
			r = rc
		}
	}
	if r == nil {
		f, err := project.Open(in.path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("not a regular file")
		}
		if err != nil {
			f.Close()
			return err
		}
		perm = info.Mode().Perm()
		r = f
	}
	defer r.Close()
	h := sha256.New()
	if err := sb.PlaceFile(in.path, io.TeeReader(cas.ContextReader(ctx, r), h), perm); err != nil {
		return err
	}
	if got := cas.NewSHA256(hex.EncodeToString(h.Sum(nil))); got != in.digest {
		return fmt.Errorf("content %s does not match planned digest %s (changed since planning?)", got, in.digest)
	}
	return nil
}
