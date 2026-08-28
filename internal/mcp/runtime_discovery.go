package mcp

import (
	"context"
	"strings"
	"time"

	kranzruntime "github.com/kranz-org/kranz/internal/runtime"
)

// RuntimeEntry is the read-only registry view exposed to an MCP client. It has
// no "current" flag: with per-call addressing there is no current runtime, and
// a field claiming otherwise would be answering a question nobody asked.
type RuntimeEntry struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Project       string                    `json:"project"`
	Directory     string                    `json:"directory"`
	Mode          string                    `json:"mode"`
	State         kranzruntime.SessionState `json:"state"`
	StartedAt     time.Time                 `json:"started_at"`
	UptimeSeconds int64                     `json:"uptime_seconds"`
	Services      *int                      `json:"services"`
	Running       *int                      `json:"running"`
	Clients       *int                      `json:"clients"`
}

type RuntimeSelectorMatch struct {
	Runtime string `json:"runtime"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

func (s *Server) runtimeEntries(ctx context.Context) ([]RuntimeEntry, error) {
	if s.runtimeListOverride != nil {
		return s.runtimeListOverride(ctx)
	}
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]RuntimeEntry, 0, len(records))
	for _, record := range records {
		entries = append(entries, RuntimeEntry{
			ID: record.ID, Name: record.Name, Project: record.Project, Directory: record.Directory,
			Mode: record.Mode, State: record.State, StartedAt: record.StartedAt,
			UptimeSeconds: max(0, int64(time.Since(record.StartedAt).Seconds())),
			Services:      record.Services, Running: record.Running, Clients: record.Clients,
		})
	}
	return entries, nil
}

// records reads the registry the resolver was built with, so tests and a
// pinned launch see the same list the resolution rules see.
func (s *Server) records(ctx context.Context) ([]kranzruntime.SessionRecord, error) {
	if s.resolver != nil && s.resolver.registry != nil {
		return s.resolver.registry.List(ctx, s.kranzVersion)
	}
	registry, err := kranzruntime.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return registry.List(ctx, s.kranzVersion)
}

// selectorMatches answers "this name exists one runtime over". excludeID is the
// runtime that already failed to find it.
func (s *Server) selectorMatches(ctx context.Context, selector, excludeID string) ([]RuntimeSelectorMatch, error) {
	if s.selectorMatchOverride != nil {
		return s.selectorMatchOverride(ctx, selector)
	}
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]RuntimeSelectorMatch, 0)
	for _, record := range records {
		if record.ID == excludeID || record.State != kranzruntime.SessionRunning {
			continue
		}
		client, dialErr := kranzruntime.DialContext(ctx, record.Socket, s.kranzVersion)
		if dialErr != nil {
			continue
		}
		cfg := client.Config()
		_ = client.Close()
		if cfg == nil {
			continue
		}
		if _, ok := cfg.Services[selector]; ok {
			matches = append(matches, RuntimeSelectorMatch{Runtime: record.Name, ID: record.ID, Kind: "service", Service: selector})
			continue
		}
		for _, name := range cfg.ServiceOrder {
			for _, tag := range cfg.Services[name].Tags {
				if strings.EqualFold(tag, selector) {
					matches = append(matches, RuntimeSelectorMatch{Runtime: record.Name, ID: record.ID, Kind: "tag", Service: name, Tag: tag})
					break
				}
			}
		}
	}
	return matches, nil
}
