# Security

goboxd runs untrusted code. The seven holes identified in the spec are all closed.

## Hole 1 — Path traversal via filename

**Where:** `internal/validate/validate.go: Filename()`

The function rejects any name that:
- is empty or longer than 128 characters
- is an absolute path (`filepath.IsAbs`)
- contains `/` or `\`
- starts with a dot
- contains NUL bytes or control characters
- does not survive `filepath.Clean` unchanged

The validator is called on both `source_filename` and `artifact_filename` before any file is written.

## Hole 2 — Shell-style directory commands

**Where:** `internal/sandbox/sandbox.go: NewJailDir()`, `WriteFile()`, `RemoveJailDir()`

All directory and file operations use the Go standard library (`os.MkdirTemp`, `os.WriteFile`, `os.RemoveAll`). There are no `exec.Command("rm", ...)` or `exec.Command("mkdir", ...)` calls anywhere in the codebase. `WriteFile` performs a containment check: it verifies the resolved destination path starts with the jail dir prefix before writing.

## Hole 3 — Compiler-flag injection

**Where:** `internal/validate/validate.go: Flags()`

Each language defines a `flag_allowlist` in `config/languages.yaml`. Client-supplied flags are filtered against this list before being passed to the build or run command. Flags not on the list are rejected with HTTP 400. The allow-list supports a trailing `*` glob (e.g. `-std=*` allows `-std=c++17`). Dangerous flags like `-fplugin=`, `-B`, `--specs=`, `-Wl,`, and `@responsefile` are never on any allow-list.

## Hole 4 — No request size limits

**Where:** `internal/api/server.go: handleRun()`

Two caps apply:

- **HTTP body cap:** `http.MaxBytesReader` is applied before `json.Decode`, limiting the raw body to `MaxSourceBytes + 4 KiB` (≈260 KiB). Any larger body causes a 400 before decoding begins.
- **Source size cap:** after decoding, `len(req.Source)` is checked against `MaxSourceBytes` (256 KiB). A test count cap (`MaxTests = 50`) is also enforced.

Inside the sandbox, `--rlimit_fsize 64` (64 MB) is passed to nsjail, capping files the sandboxed process can create.

## Hole 5 — UID collisions under load

**Where:** `internal/sandbox/sandbox.go: NewJailDir()`

Every jail directory is created with `os.MkdirTemp(base, "jail-<random8bytes>-")`. The 8-byte prefix comes from `crypto/rand.Read`, making collisions computationally infeasible. There is no shared UID counter, no retry loop, and no process-global state to collide on.

## Hole 6 — Unbounded child output

**Where:** `internal/sandbox/sandbox.go: limitedBuffer`

`limitedBuffer` implements `io.Writer` and stops accepting bytes after a configurable cap (4 MiB). Once the cap is hit, further writes are silently discarded and a `[output truncated]` marker is appended to the buffer. `cmd.Stdout` and `cmd.Stderr` are both assigned a `limitedBuffer`.

## Hole 7 — Stale jail directories

**Where (defer):** `internal/runner/runner.go: Execute()`

The first thing `Execute` does after creating the jail dir is `defer sandbox.RemoveJailDir(jailDir)`. Because `defer` runs on every return path — including panics recovered by chi's `Recoverer` middleware — cleanup is guaranteed.

**Where (startup sweep):** `cmd/goboxd/main.go`, `internal/sandbox/sandbox.go: SweepOrphans()`

At startup, `SweepOrphans` scans `JAIL_BASE_DIR` for directories matching the `jail-` prefix that are older than 15 minutes and removes them. This cleans up from any previous crash before the new process starts accepting requests.
