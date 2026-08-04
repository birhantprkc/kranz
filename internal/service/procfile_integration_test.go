package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kranz-org/kranz/internal/config"
)

func TestProcfileCommandRunsInProcfileDirectory(t *testing.T) {
	directory := t.TempDir()
	markerPath := filepath.Join(directory, "actual-cwd")
	procfilePath := filepath.Join(directory, "Procfile")
	command := "pwd > " + shellQuote(markerPath)
	if err := os.WriteFile(procfilePath, []byte("cwd: "+command+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(procfilePath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	manager := NewManager(cfg)
	t.Cleanup(func() { _ = manager.StopAll() })
	if err := manager.StartService("cwd"); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var actual []byte
	for time.Now().Before(deadline) {
		actual, err = os.ReadFile(markerPath)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("command did not create cwd marker: %v", err)
	}
	wantDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	actualDirectory, err := filepath.EvalSymlinks(strings.TrimSpace(string(actual)))
	if err != nil {
		t.Fatal(err)
	}
	if actualDirectory != wantDirectory {
		t.Errorf("command cwd = %q, want %q", actualDirectory, wantDirectory)
	}
}

func TestProcfileServiceOrderReachesManager(t *testing.T) {
	cfg := &config.Config{
		Project:      "ordered",
		Services:     map[string]config.Service{"web": {Command: "web"}, "worker": {Command: "worker"}},
		ServiceOrder: []string{"worker", "web"},
	}

	services := NewManager(cfg).Services()
	names := make([]string, 0, len(services))
	for _, managed := range services {
		names = append(names, managed.Name)
	}
	if strings.Join(names, ",") != "worker,web" {
		t.Fatalf("manager service order = %v, want [worker web]", names)
	}
}

func TestProcfileProcessEnvironmentOverridesDotenv(t *testing.T) {
	directory := t.TempDir()
	markerPath := filepath.Join(directory, "environment")
	procfilePath := filepath.Join(directory, "Procfile")
	t.Setenv("KRANZ_PROCFILE_PRECEDENCE", "from-process")
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("KRANZ_PROCFILE_PRECEDENCE=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "printf '%s' \"$KRANZ_PROCFILE_PRECEDENCE\" > " + shellQuote(markerPath)
	if err := os.WriteFile(procfilePath, []byte("env: "+command+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(procfilePath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	manager := NewManager(cfg)
	t.Cleanup(func() { _ = manager.StopAll() })
	if err := manager.StartService("env"); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}

	actual := waitForTestFile(t, markerPath)
	if string(actual) != "from-process" {
		t.Errorf("process environment = %q, want from-process", actual)
	}
}

func waitForTestFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command did not create %s", path)
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
