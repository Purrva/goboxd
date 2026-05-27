// Package sandbox wraps nsjail.  It owns the per-request jail directory
// lifecycle: creation, writing source files, building the nsjail command line
// from a typed policy, and guaranteed cleanup on every exit path.
//
// Security guarantees implemented here:
//   - Hole 2: jail dirs are created with os.MkdirTemp, never via shell commands.
//   - Hole 5: each jail gets a UUID-derived subdirectory; no UID reuse.
//   - Hole 6: stdout and stderr are capped before the caller reads them.
//   - Hole 7: cleanup runs in a defer at the right scope; orphan sweep on startup.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thesouldev/goboxd/internal/config"
)

const (
	// Maximum bytes we read from any single output stream (stdout or stderr).
	// A runaway program writing more gets the excess truncated.
	// Security hole 6: unbounded child output.
	maxOutputBytes = 4 * 1024 * 1024 // 4 MiB hard cap inside sandbox layer

	truncationMarker = "\n[output truncated]"
)

// RunResult carries everything the execution layer needs back from a single
// nsjail invocation.
type RunResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	WallTimeMs int64
	// MemoryPeakKB is best-effort; zero if nsjail does not report it.
	MemoryPeakKB int64
}

// Policy describes what nsjail should enforce for one invocation.
type Policy struct {
	// JailDir is the per-request directory that becomes the root.
	JailDir string
	// Cmd is the executable to run inside the jail.
	Cmd string
	// Args are the arguments to Cmd, already fully expanded (no templates).
	Args []string
	// Stdin is fed to the sandboxed process on its stdin.
	Stdin string
	// Limits to apply.
	Limits config.Limits
	// NsjailPath is the nsjail binary.
	NsjailPath string
}

// Exec runs one command inside nsjail according to p and returns the result.
// The context deadline is added as a hard wall on top of nsjail's own timer.
func Exec(ctx context.Context, p Policy) (*RunResult, error) {
	nsjailArgs := buildNsjailArgs(p)
	cmd := exec.CommandContext(ctx, p.NsjailPath, nsjailArgs...) //nolint:gosec

	var stdoutBuf, stderrBuf limitedBuffer
	stdoutBuf.max = maxOutputBytes
	stderrBuf.max = maxOutputBytes

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("nsjail start: %w", err)
	}

	// Write stdin in a goroutine so we never block if the child dies early.
	go func() {
		_, _ = io.WriteString(stdinPipe, p.Stdin)
		stdinPipe.Close()
	}()

	_ = cmd.Wait() // we inspect ExitCode separately; error here is expected
	wall := time.Since(start).Milliseconds()

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	return &RunResult{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		WallTimeMs: wall,
	}, nil
}

// existingBindmounts returns --bindmount_ro pairs only for paths that exist
// on the host.  /lib64 is absent on some architectures; skipping it prevents
// nsjail from refusing to start.
func existingBindmounts(paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, "--bindmount_ro", p)
		}
	}
	return out
}

// buildNsjailArgs constructs the nsjail command-line from the typed policy.
// This is deliberately not string-templated to prevent injection.
func buildNsjailArgs(p Policy) []string {
	l := p.Limits

	wallSec := l.WallTimeSec
	if wallSec <= 0 {
		wallSec = 10
	}
	memKB := l.MemoryKB
	if memKB <= 0 {
		memKB = 128 * 1024
	}
	maxProcs := l.MaxProcesses
	if maxProcs <= 0 {
		maxProcs = 64
	}

	// Build the nsjail flag portion first, then append -- cmd args.
	// All nsjail flags must come before the -- separator.
	nsjailFlags := []string{
		// nsjail 3.4 mode: run once then exit.
		"--mode", "o",
		// The jail directory becomes the chroot root.
		"--chroot", p.JailDir,
		// Drop to nobody/nogroup inside the jail.
		"--user", "65534",
		"--group", "65534",
		// Send nsjail's own log to /dev/null so it doesn't pollute stderr.
		"--log", "/dev/null",
		// Wall-clock time limit (nsjail kills the process on expiry).
		"--time_limit", strconv.Itoa(wallSec),
		// Address-space limit in MiB.
		"--rlimit_as", strconv.Itoa(memKB / 1024),
		// Process count limit.
		"--rlimit_nproc", strconv.Itoa(maxProcs),
		// Max file size inside the jail: 64 MiB.
		"--rlimit_fsize", "64",
		// Disable network namespace creation (speeds up startup).
		"--disable_clone_newnet",
		// The per-request jail dir is mounted as the writable /sandbox.
		"--bindmount", p.JailDir + ":/sandbox",
		"--cwd", "/sandbox",
	}
	// Bind-mount host directories read-only, skipping any that don't exist
	// (e.g. /lib64 is absent on some architectures).
	// These must come before the -- separator.
	nsjailFlags = append(nsjailFlags, existingBindmounts([]string{"/usr", "/lib", "/lib64", "/bin"})...)

	// The -- separator tells nsjail that what follows is the sandboxed command.
	args := append(nsjailFlags, "--", p.Cmd)
	args = append(args, p.Args...)
	return args
}

// NewJailDir creates a unique jail working directory under base.
// The caller must call RemoveJailDir when done.
//
// Security hole 5: unique dirs without UID collision.
// Security hole 2: os.MkdirTemp, not shell commands.
func NewJailDir(base string) (string, error) {
	// Generate a cryptographically random 8-byte prefix for uniqueness.
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating jail id: %w", err)
	}
	prefix := "jail-" + hex.EncodeToString(buf[:]) + "-"
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return "", fmt.Errorf("creating jail dir under %s: %w", base, err)
	}
	// The directory itself must be world-readable/executable so the sandboxed
	// nobody user can enter it.
	if err := os.Chmod(dir, 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("chmod jail dir: %w", err)
	}
	return dir, nil
}

// WriteFile writes content to name inside jailDir.
// name is validated before calling this; this function double-checks.
//
// Security hole 2: uses filepath.Join + a containment check, not shell.
func WriteFile(jailDir, name, content string) error {
	// Ensure name is just a base name with no path components.
	if filepath.Base(name) != name {
		return fmt.Errorf("WriteFile: %q is not a clean base name", name)
	}
	dest := filepath.Join(jailDir, name)
	// Containment check: the resolved dest must be inside jailDir.
	if !strings.HasPrefix(dest, jailDir+string(filepath.Separator)) {
		return fmt.Errorf("WriteFile: path escapes jail dir")
	}
	return os.WriteFile(dest, []byte(content), 0o644) //nolint:gosec
}

// RemoveJailDir deletes the jail directory and all its contents.
// It is idempotent and safe to call on a path that no longer exists.
//
// Security hole 7: always runs via defer at the call site.
func RemoveJailDir(path string) {
	_ = os.RemoveAll(path)
}

// SweepOrphans removes any jail directory under base that is older than maxAge.
// Called once at startup to clean up from a previous unclean exit.
//
// Security hole 7: startup orphan sweep.
func SweepOrphans(base string, maxAge time.Duration) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading jail base dir: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	var errs []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "jail-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			full := filepath.Join(base, e.Name())
			if err := os.RemoveAll(full); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sweeping orphans: %s", strings.Join(errs, "; "))
	}
	return nil
}


// limitedBuffer is an io.Writer that stops accepting bytes after max and
// appends a truncation marker.  It is not concurrent-safe (cmd.Stdout is
// written by a single goroutine).
type limitedBuffer struct {
	bytes.Buffer
	max     int
	capped  bool
}

func (b *limitedBuffer) Write(p []byte) (n int, err error) {
	if b.capped {
		return len(p), nil // silently discard
	}
	remaining := b.max - b.Len()
	if len(p) > remaining {
		n, err = b.Buffer.Write(p[:remaining])
		_, _ = b.Buffer.WriteString(truncationMarker)
		b.capped = true
		return len(p), err // report original length to avoid broken-pipe noise
	}
	return b.Buffer.Write(p)
}
