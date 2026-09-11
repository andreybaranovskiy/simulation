package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// newTestCompiler builds a Compiler against a temp cache, skipping the test
// when there is no Go toolchain to build with.
func newTestCompiler(t *testing.T) *Compiler {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH to compile a plugin with")
	}
	c, err := New(Options{
		CacheDir:         t.TempDir(),
		BuildTimeout:     90 * time.Second,
		MemoryLimitBytes: 512 << 20,
	})
	if err != nil {
		t.Fatalf("new compiler: %v", err)
	}
	return c
}

// The sample model is the reference an uploaded model is written against, so it
// must compile, run, and produce a definition the trusted parser accepts. This
// is the whole feature end to end, minus the run itself.
func TestSampleModelCompilesAndProducesAValidSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a program; skipped under -short")
	}

	source, err := os.ReadFile(filepath.FromSlash("../../../samples/plugin-model.go"))
	if err != nil {
		t.Fatalf("read the sample model: %v", err)
	}

	c := newTestCompiler(t)
	ctx := context.Background()

	exe, err := c.Compile(ctx, source)
	if err != nil {
		t.Fatalf("compile the sample: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("the compiled executable is missing: %v", err)
	}

	out, err := c.Generate(ctx, exe, map[string]float64{"lanes": 3, "arrivalSeconds": 40})
	if err != nil {
		t.Fatalf("generate a definition: %v", err)
	}

	parsed, err := spec.Parse(out)
	if err != nil {
		t.Fatalf("the generated definition did not parse: %v", err)
	}

	// The parameter reached the program: it asked for three lanes, so the model
	// should carry three inspection resources.
	if len(parsed.Resources) != 3 {
		t.Errorf("model has %d resources, want 3 (one per lane)", len(parsed.Resources))
	}
	if parsed.Horizon <= 0 {
		t.Errorf("model horizon is %g, want positive", parsed.Horizon)
	}
}

// A second compile of the same source must reuse the cached executable rather
// than build again: an unchanged model across a dozen scenarios should pay the
// build cost once.
func TestCompileCachesByHash(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a program; skipped under -short")
	}

	c := newTestCompiler(t)
	source := []byte("package main\nimport \"fmt\"\nfunc main(){ fmt.Print(`{\"schema\":\"sim.model/v1\"}`) }\n")

	first, err := c.Compile(context.Background(), source)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	firstTime := info.ModTime()

	second, err := c.Compile(context.Background(), source)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if second != first {
		t.Errorf("the cache returned a different path: %q then %q", first, second)
	}
	info2, _ := os.Stat(second)
	if !info2.ModTime().Equal(firstTime) {
		t.Error("the executable was rebuilt rather than reused from the cache")
	}
}

// An import outside the standard library must fail to compile, because the
// build is offline: this is what keeps an uploaded model from pulling code.
func TestNonStdlibImportIsRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a program; skipped under -short")
	}

	c := newTestCompiler(t)
	source := []byte("package main\nimport _ \"golang.org/x/crypto/bcrypt\"\nfunc main(){}\n")

	_, err := c.Compile(context.Background(), source)
	if err == nil {
		t.Fatal("a non-stdlib import compiled, but the build should be offline")
	}
	if !strings.Contains(err.Error(), "did not compile") {
		t.Errorf("unexpected error for a blocked import: %v", err)
	}
}

// A program that writes nothing is a failure, not an empty success: the caller
// must be able to tell "no definition" from "an empty definition".
func TestEmptyOutputIsAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a program; skipped under -short")
	}

	c := newTestCompiler(t)
	exe, err := c.Compile(context.Background(), []byte("package main\nfunc main(){}\n"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := c.Generate(context.Background(), exe, nil); err == nil {
		t.Fatal("a program that wrote nothing was accepted")
	}
}
