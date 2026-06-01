# Architecture

goboxd is a small HTTP service that compiles and runs untrusted code inside nsjail sandboxes. The design follows a strict layering rule: each package has one job and no layer reaches past its immediate neighbour.

## Layers

```
HTTP client
    │
    ▼
internal/api          HTTP handlers, request validation, wire types
    │
    ▼
internal/worker       Bounded concurrency pool (semaphore + WaitGroup)
    │
    ▼
internal/runner       Language-aware build/run orchestration
    │
    ▼
internal/sandbox      nsjail command builder, jail dir lifecycle
    │
    ▼
nsjail (subprocess)   Linux namespace + cgroup isolation
```

### `internal/config`

Loads `config/languages.yaml` at startup. The registry is a plain Go map indexed by language id. No Go code needs to change when a language is added; the YAML entry and an install script are enough.

### `internal/validate`

Pure functions with no I/O. Validates client-supplied filenames (security hole 1: path traversal) and filters extra flags against a per-language allow-list (security hole 3: compiler-flag injection). Tested in isolation.

### `internal/sandbox`

Wraps nsjail. Responsibilities:

- `NewJailDir`: creates a unique working directory under `JAIL_BASE_DIR` using `os.MkdirTemp` with a cryptographically random prefix. No shell commands, no UID arithmetic (holes 2, 5).
- `WriteFile`: writes source to the jail dir with a containment check.
- `Exec`: builds the nsjail command line from a typed `Policy` struct (not string templates), starts the process, caps stdout/stderr reads via `limitedBuffer` (hole 6), and returns a `RunResult`.
- `RemoveJailDir`: wraps `os.RemoveAll`, always called via `defer` in the runner (hole 7).
- `SweepOrphans`: removes stale jail dirs at startup (hole 7).

### `internal/runner`

Knows the two-phase lifecycle (build → run). For each request it:

1. Calls `sandbox.NewJailDir` and defers cleanup.
2. Writes source with `sandbox.WriteFile`.
3. If the language has a build phase, calls `sandbox.Exec` and checks the exit code.
4. For each test case, calls `sandbox.Exec` with the run command.
5. Classifies results into the spec status vocabulary.

Also contains readiness probe helpers (`ProbeNsjail`, `ProbeLang`) used by `/readyz` and `/info`.

### `internal/worker`

A channel-based semaphore with a `sync.WaitGroup`. `Pool.Run` blocks until a slot is free or the context is cancelled. The HTTP handler uses a 30-second queue-wait timeout; callers that time out get a 503 with `Retry-After: 5`.

### `internal/api`

HTTP handler (chi router). Responsibilities:

- Decodes and validates the request (body size cap, language lookup, filename validation, flag filtering).
- Acquires a worker slot via `pool.Run`.
- Calls `runner.Execute` and maps the result to the wire response.
- Updates atomic counters for `/info` stats.
- Emits one structured JSON log line per request.

### `cmd/goboxd`

Entry point. Loads config, runs the startup orphan sweep, constructs the worker pool and HTTP server, starts listening, and handles `SIGINT`/`SIGTERM` with graceful shutdown (drains in-flight work, then exits).

## Security holes addressed

| # | Hole | Where fixed |
|---|------|-------------|
| 1 | Path traversal via filename | `internal/validate/validate.go: Filename()` |
| 2 | Shell-style directory commands | `internal/sandbox/sandbox.go: NewJailDir, WriteFile` |
| 3 | Compiler-flag injection | `internal/validate/validate.go: Flags()` |
| 4 | No request size limits | `internal/api/server.go: http.MaxBytesReader, source size check` |
| 5 | UID collisions under load | `internal/sandbox/sandbox.go: NewJailDir (crypto/rand prefix)` |
| 6 | Unbounded child output | `internal/sandbox/sandbox.go: limitedBuffer` |
| 7 | Stale jail directories | `internal/runner/runner.go: defer RemoveJailDir` + `SweepOrphans` at startup |

## Concurrency model

A single `worker.Pool` with a configurable cap (default `runtime.NumCPU()`) gates all sandbox executions. Requests that arrive when the pool is full block on the pool's semaphore channel for up to 30 seconds. If the slot never opens in time (sustained overload), the handler returns 503 with a `Retry-After` header. The HTTP server has its own `ReadTimeout` and `WriteTimeout`. Shutdown drains in-flight jobs before exiting.

## Adding a language

1. Add an entry to `config/languages.yaml`.
2. Add a shell script at `scripts/lang_install/<id>.sh` that installs the toolchain from the Debian package repo.
3. `docker build` and `docker compose up`.

No Go change needed.
