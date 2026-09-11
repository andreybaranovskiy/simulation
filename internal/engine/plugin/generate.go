package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// maxSpecBytes caps what a generate step may write. A model definition is
// small; a program that streams megabytes at stdout is misbehaving, and the cap
// is what stops it filling memory before the parser ever sees it.
const maxSpecBytes = 8 << 20 // 8 MiB

// Generate runs a compiled model to produce a spec.
//
// The scenario's parameters are handed to the program on stdin as a JSON
// object, and the program is expected to write a model definition on stdout.
// Nothing it writes is trusted here: the bytes come back as-is, and the caller
// parses and validates them with the same engine every model goes through.
func (c *Compiler) Generate(ctx context.Context, exePath string, params map[string]float64) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, c.buildTO)
	defer cancel()

	input, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode the scenario parameters: %w", err)
	}

	cmd := exec.CommandContext(runCtx, exePath)
	cmd.Stdin = bytes.NewReader(input)

	var stdout limitedBuffer
	stdout.limit = maxSpecBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// The plugin lives in its own environment: it is handed nothing from the
	// server's, so a secret in an environment variable cannot leak into
	// uploaded code.
	cmd.Env = []string{}
	newProcessGroup(cmd)

	if err := runGuarded(cmd, c.memBytes); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("the model took too long to produce a definition")
		}
		if stdout.overflow {
			return nil, fmt.Errorf("the model wrote more than %d bytes", maxSpecBytes)
		}
		return nil, fmt.Errorf("the model failed to produce a definition:\n%s", compilerMessage(stderr.String()))
	}

	out := stdout.Bytes()
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("the model produced no definition on its output")
	}
	return out, nil
}

// limitedBuffer is a bytes.Buffer that stops accepting data past a limit and
// remembers that it did, so an oversized write fails with a clear reason rather
// than an out-of-memory crash of the server.
type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.overflow {
		// Keep draining the pipe so the child is not blocked on a full stdout,
		// which would leave it hanging until the timeout instead of exiting.
		return len(p), nil
	}
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// runGuarded starts a command, applies the memory cap once it has a pid, and
// waits for it. The small window between start and cap is covered by process
// setup, well before an uploaded program has allocated anything.
func runGuarded(cmd *exec.Cmd, memBytes int64) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	release, err := limitProcess(cmd.Process.Pid, memBytes)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("bound the plugin process: %w", err)
	}
	defer release()

	return cmd.Wait()
}

// buildDeadline is a small helper kept for symmetry with the runner, so a
// future caller can reason about the generate budget without reaching into the
// Compiler's fields.
func (c *Compiler) buildDeadline() time.Duration { return c.buildTO }
