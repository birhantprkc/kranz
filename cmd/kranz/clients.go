package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	kranzcli "github.com/kranz-org/kranz/internal/cli"
	kranzruntime "github.com/kranz-org/kranz/internal/runtime"
)

// clientRow is one attached client, named together with the runtime it is
// attached to. Runtimes and their clients are different kinds of thing —
// `ps` lists what is running, this lists who is working in it — so they are
// two commands rather than one table with a mode column doing both jobs.
type clientRow struct {
	Runtime string    `json:"runtime"`
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Surface string    `json:"surface"`
	Label   string    `json:"label"`
	PID     int       `json:"pid"`
	Version string    `json:"version"`
	Since   time.Time `json:"connected_at"`
}

func runClients(options kranzcli.GlobalOptions, stdout, stderr io.Writer) int {
	registry, err := kranzruntime.DefaultRegistry()
	if err != nil {
		return kranzcli.WriteError(stdout, stderr, options.Output, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	records, err := registry.List(ctx, version)
	if err != nil {
		return kranzcli.WriteError(stdout, stderr, options.Output, err)
	}
	rows := make([]clientRow, 0)
	for _, record := range records {
		if options.Project != "" && !matchesRuntimeReference(record, options.Project) {
			continue
		}
		if record.State != kranzruntime.SessionRunning {
			continue
		}
		client, dialErr := kranzruntime.DialContext(ctx, record.Socket, version)
		if dialErr != nil {
			continue
		}
		connected, clientsErr := client.Clients()
		_ = client.Close()
		if clientsErr != nil {
			continue
		}
		for _, entry := range connected {
			// This listing holds a connection of its own; reporting it would
			// mean every runtime always has at least one client.
			if entry.PID == ownPID() {
				continue
			}
			rows = append(rows, clientRow{
				Runtime: record.Name, ID: record.ID, Project: record.Project,
				Surface: entry.Surface, Label: entry.Label, PID: entry.PID,
				Version: entry.Version, Since: entry.ConnectedAt,
			})
		}
	}
	if options.Output == kranzcli.OutputJSON {
		if err := kranzcli.WriteJSON(stdout, rows); err != nil {
			return kranzcli.WriteError(stdout, stderr, options.Output, err)
		}
		return 0
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(stdout, "No client is attached to a Kranz runtime.")
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "RUNTIME\tSURFACE\tCLIENT\tPID\tCONNECTED")
	for _, row := range rows {
		label := row.Label
		if label == "" {
			label = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", row.Runtime, surfaceLabel(row.Surface), label, row.PID, shortDuration(time.Since(row.Since)))
	}
	if err := w.Flush(); err != nil {
		return kranzcli.WriteError(stdout, stderr, options.Output, err)
	}
	return 0
}

// ownPID names this process so the listing can exclude its own probe.
func ownPID() int { return os.Getpid() }

func surfaceLabel(surface string) string {
	if surface == "" {
		return "unknown"
	}
	return surface
}

func matchesRuntimeReference(record kranzruntime.SessionRecord, reference string) bool {
	return record.Name == reference || record.ID == reference || strings.HasPrefix(record.ID, reference)
}
