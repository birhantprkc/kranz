package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kranz-org/kranz/internal/config"
	"github.com/kranz-org/kranz/internal/port"
	"github.com/kranz-org/kranz/internal/service"
)

// Preflight statuses, ordered by how much they should worry a reader.
const (
	PreflightOK   = "ok"
	PreflightWarn = "warn"
	PreflightFail = "fail"
)

// PreflightFinding is one checked fact about a project.
type PreflightFinding struct {
	Check   string `json:"check"`
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

// PreflightResult is the complete preflight answer, counted so a caller does
// not have to re-derive the verdict from the findings.
type PreflightResult struct {
	Findings        []PreflightFinding `json:"findings"`
	ServicesChecked int                `json:"services_checked"`
	Problems        int                `json:"problems"`
	Warnings        int                `json:"warnings"`
}

// Preflight checks a loaded configuration against the filesystem and the local
// ports it declares. It lives in the application layer because "why can this
// service not start" is the same question whether a person types `kranz
// doctor` or an agent asks the runtime, and two implementations of it would
// eventually answer differently.
//
// It examines the configuration and the machine, never the running services:
// checker may be nil, in which case declared ports are not probed.
func Preflight(cfg *config.Config, paths []string, directory string, checker port.Checker) PreflightResult {
	result := PreflightResult{Findings: []PreflightFinding{}, ServicesChecked: len(cfg.Services)}
	record := func(check, subject, status, detail string) {
		result.Findings = append(result.Findings, PreflightFinding{Check: check, Subject: subject, Status: status, Detail: detail})
	}

	record("config", strings.Join(paths, ", "), PreflightOK, fmt.Sprintf("%d services, %d actions", len(cfg.Services), len(cfg.ActionIDs())))
	for _, diagnostic := range cfg.Diagnostics {
		record("config", "diagnostic", PreflightWarn, diagnostic)
	}
	if _, err := service.TopologicalOrder(cfg); err != nil {
		record("dependencies", "graph", PreflightFail, err.Error())
	} else {
		record("dependencies", "graph", PreflightOK, "no cycles")
	}

	var declaredPorts []int
	for _, name := range cfg.ServiceNames() {
		svc := cfg.Services[name]
		declaredPorts = append(declaredPorts, svc.Ports...)

		if svc.Dir != "" {
			resolved := resolvePath(directory, svc.Dir)
			if info, statErr := os.Stat(resolved); statErr != nil || !info.IsDir() {
				record("directory", name, PreflightFail, fmt.Sprintf("%s is not a directory", svc.Dir))
			}
		}
		// The loader resolves env files against the service directory and
		// tolerates a missing one, so an absent file is a warning about
		// variables that will silently not arrive, not a hard failure.
		for _, envFile := range config.ServiceEnvFiles(cfg, svc) {
			if _, statErr := os.Stat(resolvePath(directory, envFile)); statErr != nil {
				record("env_file", name, PreflightWarn, fmt.Sprintf("%s is missing; its variables will not be set", envFile))
			}
		}
		if svc.Command == "" && svc.Lifecycle.Start.Command == "" {
			record("command", name, PreflightWarn, "no start command")
			continue
		}
		shell := svc.Shell
		if shell == "" {
			shell = cfg.Defaults.Shell
		}
		if shell != "" {
			if _, lookErr := exec.LookPath(shell); lookErr != nil {
				record("shell", name, PreflightFail, fmt.Sprintf("%s is not executable", shell))
			}
		}
	}

	if len(declaredPorts) > 0 && checker != nil {
		listeners, portErr := checker.CheckPorts(declaredPorts)
		if portErr != nil {
			record("ports", "check", PreflightWarn, portErr.Error())
		} else {
			for _, name := range cfg.ServiceNames() {
				for _, number := range cfg.Services[name].Ports {
					if info := listeners[number]; info != nil {
						record("port", fmt.Sprintf("%s:%d", name, number), PreflightWarn, fmt.Sprintf("already held by %s (PID %d)", info.Process, info.PID))
					}
				}
			}
		}
	}

	for _, finding := range result.Findings {
		switch finding.Status {
		case PreflightFail:
			result.Problems++
		case PreflightWarn:
			result.Warnings++
		}
	}
	return result
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// Preflight implements API.Preflight against the runtime's loaded project.
func (l *Local) Preflight() PreflightResult {
	project := l.Project()
	directory, err := os.Getwd()
	if err != nil {
		directory = "."
	}
	l.portMu.RLock()
	checker := l.portChecker
	l.portMu.RUnlock()
	return Preflight(l.Config(), project.ConfigPaths, directory, checker)
}
