// Package plugin compiles and runs uploaded Go models.
//
// It exists behind a gate that ships closed: the feature is off unless an
// operator turns it on, and even then only administrators may upload. The
// reason is in the name of the risk it carries — compiling and running code a
// user supplied is arbitrary code execution on the server, and no sandbox on
// Windows reduces that to zero. This package narrows it as far as it
// reasonably can and documents the rest in deploy/iis/SECURITY.md.
//
// The safety comes from what an uploaded program is allowed to be and to do,
// not from trusting it. An uploaded model is a plain Go program that reads the
// scenario's parameters on stdin and writes a model definition — the same
// declarative spec the built-in templates produce — on stdout. The untrusted
// code therefore only ever produces data. That data is then parsed and run by
// the same trusted engine every other model uses, so nothing a plugin writes
// ever reaches the trace writer, the artifact pipeline or the database except
// as a validated model definition.
//
// Compilation is stdlib-only and offline: the build runs with the module proxy
// switched off and cgo disabled, so an uploaded program cannot pull a
// dependency, run a C compiler, or reach the network at build time. Both the
// build and the generate step run under a memory cap and a timeout, in their
// own process group, so a runaway plugin is bounded and killable.
package plugin

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Compiler builds and runs uploaded models. One is created at startup when the
// feature is enabled, and nil everywhere it is not, so the gate is impossible
// to forget: a caller with a nil Compiler cannot run a plugin.
type Compiler struct {
	goTool    string
	cacheDir  string
	buildTO   time.Duration
	memBytes  int64
	log       *slog.Logger
}

// Options configures a Compiler.
type Options struct {
	// GoTool is the go toolchain to build with. Empty resolves to "go" on PATH.
	GoTool string
	// CacheDir holds compiled plugin executables, keyed by source hash so an
	// unchanged model is built once and then reused across runs.
	CacheDir string
	// BuildTimeout bounds a single compilation.
	BuildTimeout time.Duration
	// MemoryLimitBytes caps the build and the generate step. Zero disables the
	// cap, which is not recommended for a feature that runs uploaded code.
	MemoryLimitBytes int64
	Log              *slog.Logger
}

// New validates the toolchain and prepares the cache. Doing this at startup
// means a misconfigured plugin feature is reported when the server starts, not
// when the first upload is run.
func New(opts Options) (*Compiler, error) {
	goTool := opts.GoTool
	if goTool == "" {
		goTool = "go"
	}
	resolved, err := exec.LookPath(goTool)
	if err != nil {
		return nil, fmt.Errorf("the Go toolchain %q was not found: %w", goTool, err)
	}

	if opts.CacheDir == "" {
		return nil, fmt.Errorf("a plugin cache directory is required")
	}
	if err := os.MkdirAll(opts.CacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create the plugin cache: %w", err)
	}

	buildTO := opts.BuildTimeout
	if buildTO <= 0 {
		buildTO = 2 * time.Minute
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("uploaded Go models enabled", "toolchain", resolved, "cache", opts.CacheDir)

	return &Compiler{
		goTool:   resolved,
		cacheDir: opts.CacheDir,
		buildTO:  buildTO,
		memBytes: opts.MemoryLimitBytes,
		log:      log,
	}, nil
}

// exePath is where a source hash's compiled executable lives.
func (c *Compiler) exePath(hash string) string {
	return filepath.Join(c.cacheDir, hash+exeSuffix())
}
