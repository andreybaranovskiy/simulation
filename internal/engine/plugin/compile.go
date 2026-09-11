package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// maxSourceBytes caps an uploaded model. A model definition is small; anything
// approaching this is either a mistake or an attempt to exhaust the compiler.
const maxSourceBytes = 1 << 20 // 1 MiB

// Compile builds an uploaded model into an executable and returns its path.
//
// The result is cached by the hash of the source, so a model that has not
// changed is compiled once and then reused: a comparison that runs the same
// uploaded model across a dozen scenarios pays the build cost a single time.
func (c *Compiler) Compile(ctx context.Context, source []byte) (string, error) {
	if len(source) == 0 {
		return "", fmt.Errorf("the uploaded model is empty")
	}
	if len(source) > maxSourceBytes {
		return "", fmt.Errorf("the uploaded model is larger than %d bytes", maxSourceBytes)
	}

	sum := sha256.Sum256(source)
	hash := hex.EncodeToString(sum[:])
	exe := c.exePath(hash)

	// A cached executable is trusted only because its name is the hash of the
	// exact source that produced it: a different source cannot collide onto it.
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}

	buildDir, err := os.MkdirTemp(c.cacheDir, "build-")
	if err != nil {
		return "", fmt.Errorf("create a build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), source, 0o600); err != nil {
		return "", fmt.Errorf("write the model source: %w", err)
	}
	// A module with no requirements is what keeps the build stdlib-only: with
	// the proxy off, any non-stdlib import fails to resolve rather than
	// reaching out for a download.
	gomod := fmt.Sprintf("module simplugin\n\ngo %s\n", runtime.Version()[2:6])
	if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(gomod), 0o600); err != nil {
		return "", fmt.Errorf("write the build module: %w", err)
	}

	tmpExe := exe + ".tmp"
	buildCtx, cancel := context.WithTimeout(ctx, c.buildTO)
	defer cancel()

	cmd := exec.CommandContext(buildCtx, c.goTool, "build", "-trimpath", "-o", tmpExe, ".")
	cmd.Dir = buildDir
	cmd.Env = buildEnv(buildDir)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	newProcessGroup(cmd)

	if err := runGuarded(cmd, c.memBytes); err != nil {
		_ = os.Remove(tmpExe)
		if buildCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("the model took too long to compile")
		}
		return "", fmt.Errorf("the model did not compile:\n%s", compilerMessage(stderr.String()))
	}

	// Rename into place only after a clean build, so the cache never holds a
	// half-written executable that a later run would trust by its name.
	if err := os.Rename(tmpExe, exe); err != nil {
		_ = os.Remove(tmpExe)
		return "", fmt.Errorf("cache the compiled model: %w", err)
	}

	c.log.Info("compiled an uploaded model", "hash", hash[:12], "bytes", len(source))
	return exe, nil
}

// buildEnv is the compilation environment: the current one, with the controls
// that make the build offline and stdlib-only forced on.
func buildEnv(buildDir string) []string {
	env := append(os.Environ(),
		// No module downloads: an import outside the standard library fails
		// here rather than fetching code.
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		// No cgo: an uploaded program cannot invoke a C compiler, which would
		// be arbitrary code at build time outside the Go toolchain.
		"CGO_ENABLED=0",
		// Never switch toolchains on a directive in the module: the build uses
		// exactly the toolchain the operator installed.
		"GOTOOLCHAIN=local",
		// A private build and module cache under the build directory keeps one
		// compilation from disturbing another or the operator's own caches.
		"GOCACHE="+filepath.Join(buildDir, ".gocache"),
		"GOMODCACHE="+filepath.Join(buildDir, ".gomodcache"),
	)
	return env
}

// compilerMessage trims a build's stderr to something a user can act on without
// leaking server paths. The build directory is a temp path they never chose, so
// the message is more useful with it removed.
func compilerMessage(text string) string {
	const limit = 4000
	if len(text) > limit {
		text = text[:limit] + "\n… (truncated)"
	}
	return text
}
