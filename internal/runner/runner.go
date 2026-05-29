// Package runner knows how to compile (if needed) and run a single language
// using the sandbox layer.  It translates language config and a run request
// into one or two sandbox.Exec calls and normalises the result into the
// canonical API types.
package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/sandbox"
	"github.com/thesouldev/goboxd/internal/validate"
)

// BuildResult is the result of the compilation phase.
type BuildResult struct {
	Status     string // "ok" | "failed" | "internal_error"
	Stdout     string
	Stderr     string
	DurationMs int64
}

// TestResult is the result of running the program with one test case.
type TestResult struct {
	Status       string // see spec status vocabulary
	Stdout       string
	Stderr       string
	DurationMs   int64
	MemoryPeakKB int64
}

// RunRequest is the fully parsed and validated request from the API layer.
type RunRequest struct {
	Lang               *config.Language
	Source             string
	SourceFilename     string
	ArtifactFilename   string
	ExtraBuildFlags    []string
	ExtraRunFlags      []string
	BuildLimitOverride *config.Limits
	RunLimitOverride   *config.Limits
	Tests              []TestCase
	NsjailPath         string
	JailBaseDir        string
}

// TestCase is one (stdin, expected_stdout) pair.
type TestCase struct {
	Stdin          string
	ExpectedStdout string
}

// RunResponse is what the runner returns to the API layer.
type RunResponse struct {
	Build BuildResult
	Tests []TestResult
}

// Execute runs the full lifecycle for one request:
// 1. create a jail dir
// 2. write source
// 3. build (if the language has a build phase)
// 4. run each test case
// 5. clean up the jail dir in a defer
func Execute(ctx context.Context, req RunRequest) (RunResponse, error) {
	jailDir, err := sandbox.NewJailDir(req.JailBaseDir)
	if err != nil {
		return RunResponse{}, fmt.Errorf("creating jail: %w", err)
	}
	// Security hole 7: defer guarantees cleanup on every exit path.
	defer sandbox.RemoveJailDir(jailDir)

	if err := sandbox.WriteFile(jailDir, req.SourceFilename, req.Source); err != nil {
		return RunResponse{}, fmt.Errorf("writing source: %w", err)
	}

	var resp RunResponse

	// --- Build phase (compiled languages only) ---
	if req.Lang.Build != nil {
		buildLimits := mergeLimits(req.Lang.Build.Limits, req.BuildLimitOverride)
		buildArgs, err := expandArgs(req.Lang.Build.Args, req.SourceFilename, req.ArtifactFilename, req.ExtraBuildFlags)
		if err != nil {
			return RunResponse{}, err
		}

		result, err := sandbox.Exec(ctx, sandbox.Policy{
			JailDir:    jailDir,
			Cmd:        req.Lang.Build.Cmd,
			Args:       buildArgs,
			Limits:     buildLimits,
			NsjailPath: req.NsjailPath,
		})
		if err != nil {
			resp.Build = BuildResult{
				Status: "internal_error",
				Stderr: err.Error(),
			}
			for range req.Tests {
				resp.Tests = append(resp.Tests, TestResult{Status: "not_executed"})
			}
			return resp, nil
		}

		if result.ExitCode != 0 {
			resp.Build = BuildResult{
				Status:     "failed",
				Stdout:     result.Stdout,
				Stderr:     result.Stderr,
				DurationMs: result.WallTimeMs,
			}
			for range req.Tests {
				resp.Tests = append(resp.Tests, TestResult{Status: "not_executed"})
			}
			return resp, nil
		}

		resp.Build = BuildResult{
			Status:     "ok",
			Stdout:     result.Stdout,
			Stderr:     result.Stderr,
			DurationMs: result.WallTimeMs,
		}
	} else {
		// Interpreted: no build phase.
		resp.Build = BuildResult{Status: "ok"}
	}

	// --- Run phase ---
	runLimits := mergeLimits(req.Lang.Run.Limits, req.RunLimitOverride)
	runArgs, err := expandArgs(req.Lang.Run.Args, req.SourceFilename, req.ArtifactFilename, req.ExtraRunFlags)
	if err != nil {
		return RunResponse{}, err
	}
	runCmd := expandTemplate(req.Lang.Run.Cmd, req.SourceFilename, req.ArtifactFilename)

	for _, tc := range req.Tests {
		tr, execErr := runOneTest(ctx, jailDir, runCmd, runArgs, tc, runLimits, req.NsjailPath)
		if execErr != nil {
			resp.Tests = append(resp.Tests, TestResult{Status: "internal_error", Stderr: execErr.Error()})
			continue
		}
		resp.Tests = append(resp.Tests, tr)
	}

	return resp, nil
}

func runOneTest(
	ctx context.Context,
	jailDir, cmd string,
	args []string,
	tc TestCase,
	limits config.Limits,
	nsjailPath string,
) (TestResult, error) {
	result, err := sandbox.Exec(ctx, sandbox.Policy{
		JailDir:    jailDir,
		Cmd:        cmd,
		Args:       args,
		Stdin:      tc.Stdin,
		Limits:     limits,
		NsjailPath: nsjailPath,
	})
	if err != nil {
		return TestResult{}, err
	}

	status := classifyResult(result, tc.ExpectedStdout, limits)
	return TestResult{
		Status:       status,
		Stdout:       result.Stdout,
		Stderr:       result.Stderr,
		DurationMs:   result.WallTimeMs,
		MemoryPeakKB: result.MemoryPeakKB,
	}, nil
}

// classifyResult maps a sandbox run to the official lowercase goboxd contract strings.
// It safely consumes non-zero exit codes without bubbling up system exceptions.
func classifyResult(r *sandbox.RunResult, expected string, limits config.Limits) string {
	// 1. time_exceeded: Parse explicit nsjail timeout log signatures or SIGKILL/SIGTERM caps
	if strings.Contains(r.Stderr, "run time >= time limit") || r.ExitCode == 143 || r.ExitCode == 137 {
		return "time_exceeded"
	}

	// 2. runtime_error: Process explicit signal termination logs or unexpected non-zero crashes
	if strings.Contains(r.Stderr, "terminated with signal") || (r.ExitCode != 0) {
		return "runtime_error"
	}

	// 3. accepted: Strict byte-for-byte matching
	if r.Stdout == expected {
		return "accepted"
	}

	// 4. output_whitespace_mismatch: Fallback evaluation using a full trim of leading/trailing spaces
	if strings.TrimSpace(r.Stdout) == strings.TrimSpace(expected) {
		return "output_whitespace_mismatch"
	}

	// 5. wrong_output: Process completed cleanly with status 0, but output strings completely disagree
	return "wrong_output"
}

// expandArgs replaces placeholder tokens in an args list.
func expandArgs(args []string, source, artifact string, extraFlags []string) ([]string, error) {
	out := make([]string, 0, len(args)+len(extraFlags))
	for _, a := range args {
		switch a {
		case "{{flags}}":
			out = append(out, extraFlags...)
		case "{{source}}":
			if err := validate.Filename(filepath.Base(source)); err != nil {
				return nil, fmt.Errorf("expanding {{source}}: %w", err)
			}
			out = append(out, filepath.Base(source))
		case "{{artifact}}":
			out = append(out, filepath.Base(artifact))
		default:
			out = append(out, expandTemplate(a, source, artifact))
		}
	}
	return out, nil
}

// expandTemplate replaces tokens inside a single string.
func expandTemplate(s, source, artifact string) string {
	s = strings.ReplaceAll(s, "{{source}}", filepath.Base(source))
	s = strings.ReplaceAll(s, "{{artifact}}", filepath.Base(artifact))
	return s
}

// mergeLimits overlays override onto the language default.
func mergeLimits(def config.Limits, override *config.Limits) config.Limits {
	if override == nil {
		return def
	}
	out := def
	if override.WallTimeSec > 0 {
		out.WallTimeSec = override.WallTimeSec
	}
	if override.MemoryKB > 0 {
		out.MemoryKB = override.MemoryKB
	}
	if override.MaxProcesses > 0 {
		out.MaxProcesses = override.MaxProcesses
	}
	return out
}

// TopLevelStatus computes the spec-defined top-level status from build and
// per-test results.
// TopLevelStatus computes the spec-defined top-level status matching uppercase enums.
func TopLevelStatus(build BuildResult, tests []TestResult) string {
	// Rule A: If compile layer failed, immediate abort cascade
	if build.Status != "ok" && build.Status != "accepted" { 
		return "build_failed"
	}

	// Rule B: Step sequentially to find the first non-accepted anomaly
	for _, t := range tests {
		if t.Status != "accepted" {
			return t.Status // Returns the exact lowercase status of the first failing case
		}
	}

	// Rule C: Pure green pass across all verification check gates
	return "accepted"
}

// TruncateOutput caps a string at maxBytes and appends a marker if needed.
func TruncateOutput(s string, maxBytes int) string {
	if !utf8.ValidString(s) {
		if len(s) > maxBytes {
			return s[:maxBytes] + "\n[output truncated]"
		}
		return s
	}
	if len(s) <= maxBytes {
		return s
	}
	n := 0
	for i := range s {
		if i >= maxBytes {
			break
		}
		n = i
	}
	return s[:n] + "\n[output truncated]"
}