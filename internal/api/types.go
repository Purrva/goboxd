package api

// Build types for API

type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

type HealthResponse struct {
	Status string `json:"status"`
}

type LangStatus struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type NsjailStatus struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ReadyzResponse struct {
	Status    string                `json:"status"`
	Nsjail    NsjailStatus          `json:"nsjail"`
	Languages map[string]LangStatus `json:"languages"`
}

type GlobalLimits struct {
	MaxSourceBytes    int64 `json:"max_source_bytes"`
	MaxTests          int   `json:"max_tests"`
	MaxConcurrentJobs int   `json:"max_concurrent_jobs"`
}

type ServiceStats struct {
	InFlightJobs       int    `json:"in_flight_jobs"`
	JobsTotal          int64  `json:"jobs_total"`
	JobsFailedInternal int64  `json:"jobs_failed_internal"`
	LastInternalErrorAt string `json:"last_internal_error_at,omitempty"`
	DiskFreeBytes      int64 `json:"disk_free_bytes"`
}

type LimitInfo struct {
	WallTimeSec  int `json:"wall_time_sec"`
	MemoryKB     int `json:"memory_kb"`
	MaxProcesses int `json:"max_processes"`
}

type LangInfo struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Version          string    `json:"version"`
	DefaultRunLimits LimitInfo `json:"default_run_limits"`
}

type InfoResponse struct {
	BuildInfo BuildInfo             `json:"build_info"`
	Nsjail    NsjailStatus          `json:"nsjail"`
	Languages []LangInfo            `json:"languages"`
	Limits    GlobalLimits          `json:"limits"`
	Stats     ServiceStats          `json:"stats"`
}

type RequestLimits struct {
	WallTimeSec  int `json:"wall_time_sec,omitempty"`
	MemoryKB     int `json:"memory_kb,omitempty"`
	MaxProcesses int `json:"max_processes,omitempty"`
}

type PhaseRequest struct {
	Flags  []string       `json:"flags,omitempty"`
	Limits *RequestLimits `json:"limits,omitempty"`
}

type TestCase struct {
	Stdin          string `json:"stdin"`
	ExpectedStdout string `json:"expected_stdout"`
}

type RunRequest struct {
	Language         string        `json:"language"`
	Source           string        `json:"source"`
	SourceFilename   string        `json:"source_filename,omitempty"`
	ArtifactFilename string        `json:"artifact_filename,omitempty"`
	Build            *PhaseRequest `json:"build,omitempty"`
	Run              *PhaseRequest `json:"run,omitempty"`
	Tests            []TestCase    `json:"tests"`
}

type BuildResponse struct {
	Status     string `json:"status"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

type TestResult struct {
	Status       string `json:"status"`
	Stdout       string `json:"stdout,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	MemoryPeakKB int64  `json:"memory_peak_kb"`
}

type RunResponse struct {
	Status string        `json:"status"`
	Build  BuildResponse `json:"build"`
	Tests  []TestResult  `json:"tests"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
