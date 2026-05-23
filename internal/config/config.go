// Package config loads and validates the service configuration from a YAML file
// and environment variables.  Languages are defined entirely in YAML; no Go
// code change is needed to add a new language.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Limits describes resource caps for a single build or run phase.
type Limits struct {
	WallTimeSec  int `yaml:"wall_time_s"`
	MemoryKB     int `yaml:"memory_kb"`
	MaxProcesses int `yaml:"max_processes"`
}

// Phase holds the command, argument template, default limits, and allowed
// extra flags for one phase (build or run) of a language.
type Phase struct {
	Cmd           string   `yaml:"cmd"`
	Args          []string `yaml:"args"`
	Limits        Limits   `yaml:"limits"`
	FlagAllowlist []string `yaml:"flag_allowlist"`
}

// Language is one entry in the language registry.
type Language struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`

	// SourceFilenameStrategy controls how the source filename is chosen.
	// "from_request" means the caller must supply it; otherwise the static
	// value in SourceFilename is used.
	SourceFilenameStrategy   string `yaml:"source_filename_strategy"`
	ArtifactFilenameStrategy string `yaml:"artifact_filename_strategy"`

	SourceFilename   string `yaml:"source_filename"`
	ArtifactFilename string `yaml:"artifact_filename"`

	Build *Phase `yaml:"build"` // nil for interpreted languages
	Run   Phase  `yaml:"run"`

	// ReadinessProbe overrides the default "--version" check.
	ReadinessProbe string `yaml:"readiness_probe"`
}

// languageFile is the on-disk YAML shape.
type languageFile struct {
	Languages []Language `yaml:"languages"`
}

// Config is the resolved, validated service configuration.
type Config struct {
	Port              int
	MaxConcurrentJobs int
	JailBaseDir       string
	NsjailPath        string
	OrphanMaxAge      time.Duration
	MaxSourceBytes    int64
	MaxTests          int
	MaxOutputBytes    int64

	// LangConfigPath is the file that was loaded.
	LangConfigPath string

	// Languages is the registry indexed by language id.
	Languages map[string]*Language
}

// Load reads the YAML language config and any relevant env overrides.
func Load() (*Config, error) {
	cfgPath := envOr("LANG_CONFIG", "/etc/goboxd/languages.yaml")

	data, err := os.ReadFile(cfgPath) //nolint:gosec // path comes from env, not user input
	if err != nil {
		return nil, fmt.Errorf("reading language config %s: %w", cfgPath, err)
	}

	var lf languageFile
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing language config: %w", err)
	}

	langMap := make(map[string]*Language, len(lf.Languages))
	for i := range lf.Languages {
		l := &lf.Languages[i]
		if l.ID == "" {
			return nil, fmt.Errorf("language entry %d has no id", i)
		}
		if _, dup := langMap[l.ID]; dup {
			return nil, fmt.Errorf("duplicate language id %q", l.ID)
		}
		langMap[l.ID] = l
	}

	maxConcurrent := runtime.NumCPU()
	if v := os.Getenv("MAX_CONCURRENT_JOBS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("MAX_CONCURRENT_JOBS must be a positive integer, got %q", v)
		}
		maxConcurrent = n
	}

	port := 8080
	if v := os.Getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("PORT must be 1-65535, got %q", v)
		}
		port = n
	}

	jailBase := envOr("JAIL_BASE_DIR", "/tmp/goboxd-jails")
	if err := os.MkdirAll(jailBase, 0o700); err != nil {
		return nil, fmt.Errorf("creating jail base dir %s: %w", jailBase, err)
	}

	// Resolve to absolute path to guarantee no relative-path weirdness later.
	absJail, err := filepath.Abs(jailBase)
	if err != nil {
		return nil, fmt.Errorf("resolving jail base dir: %w", err)
	}

	return &Config{
		Port:              port,
		MaxConcurrentJobs: maxConcurrent,
		JailBaseDir:       absJail,
		NsjailPath:        envOr("NSJAIL_PATH", "/usr/local/bin/nsjail"),
		OrphanMaxAge:      15 * time.Minute,
		MaxSourceBytes:    256 * 1024, // 256 KiB
		MaxTests:          50,
		MaxOutputBytes:    1 * 1024 * 1024, // 1 MiB per stream
		LangConfigPath:    cfgPath,
		Languages:         langMap,
	}, nil
}

// Validate performs consistency checks that can't be expressed in the YAML
// schema.
func (c *Config) Validate() error {
	for id, l := range c.Languages {
		if l.Run.Cmd == "" {
			return fmt.Errorf("language %q: run.cmd is required", id)
		}
		if l.Build != nil && l.Build.Cmd == "" {
			return fmt.Errorf("language %q: build block present but cmd is empty", id)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
