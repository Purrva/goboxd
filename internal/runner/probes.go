package runner

import (
	"os/exec"
	"strings"

	"github.com/thesouldev/goboxd/internal/config"
)

type ProbeResult struct {
	OK      bool
	Version string
	Error   string
}

func ProbeNsjail(path string) ProbeResult {
	cmd := exec.Command(path, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}
	}
	return ProbeResult{OK: true, Version: strings.TrimSpace(string(out))}
}

func ProbeLang(lang *config.Language) ProbeResult {
	probeCmd := lang.ReadinessProbe
	if probeCmd == "" {
		// fallback if none specified
		probeCmd = lang.Run.Cmd + " --version"
	}
	parts := strings.Split(probeCmd, " ")
	cmd := exec.Command(parts[0], parts[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}
	}
	return ProbeResult{OK: true, Version: strings.TrimSpace(string(out))}
}
