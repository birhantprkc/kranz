package app

import (
	"testing"

	"github.com/kranz-org/kranz/internal/config"
)

func TestGraphCarriesDependencyPrerequisiteAndOwnershipEdges(t *testing.T) {
	cfg := &config.Config{
		Project:      "Graph",
		ServiceOrder: []string{"db", "api"},
		Services: map[string]config.Service{
			"db": {Command: "sleep 30", Shell: "/bin/sh"},
			"api": {
				Command: "sleep 30", Shell: "/bin/sh", DependsOn: []string{"db"},
				DependencyConditions: map[string]config.DependencyConfig{"db": {Condition: config.DependencyHealthy}},
				BeforeStart:          []config.Prerequisite{{Action: "migrate", Run: config.PrerequisiteAlways}},
				Actions:              map[string]config.Action{"migrate": {Command: "true", Shell: "/bin/sh"}},
				ActionOrder:          []string{"migrate"},
			},
		},
		ActionGroups:     map[string]config.ActionGroup{"tools": {Description: "project tasks", Actions: map[string]config.Action{"fmt": {Command: "true"}}, ActionOrder: []string{"fmt"}}},
		ActionGroupOrder: []string{"tools"},
	}
	local := NewLocal(cfg, nil, Options{SessionID: "session-graph"})
	t.Cleanup(func() { _ = local.Shutdown() })
	local.SetServiceStatusForTest("db", config.StatusStarting)

	graph := local.Graph()
	kinds := map[string]string{}
	for _, node := range graph.Nodes {
		kinds[node.ID] = node.Kind
	}
	for id, want := range map[string]string{"db": GraphNodeService, "api": GraphNodeService, "tools": GraphNodeGroup, "api/migrate": GraphNodeAction, "tools/fmt": GraphNodeAction} {
		if kinds[id] != want {
			t.Errorf("node %s = %q, want %q", id, kinds[id], want)
		}
	}
	// Live state travels with the graph so a reader does not have to join it
	// against a separate status call to see where a chain is stuck.
	for _, node := range graph.Nodes {
		if node.ID == "db" && node.State != config.StatusStarting {
			t.Errorf("db state = %q", node.State)
		}
	}

	var dependency, prerequisite, owns bool
	for _, edge := range graph.Edges {
		switch {
		case edge.Type == GraphEdgeDependency && edge.From == "db" && edge.To == "api":
			dependency = edge.Condition == string(config.DependencyHealthy)
		case edge.Type == GraphEdgePrerequisite && edge.From == "api/migrate" && edge.To == "api":
			prerequisite = edge.Run == string(config.PrerequisiteAlways)
		case edge.Type == GraphEdgeOwns && edge.From == "tools" && edge.To == "tools/fmt":
			owns = true
		}
	}
	if !dependency || !prerequisite || !owns {
		t.Fatalf("edges = %#v", graph.Edges)
	}
}
