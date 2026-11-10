package main

import (
	"errors"
	"testing"

	"github.com/sensu/sensu-plugin-sdk/sensu"

	"github.com/opsone/sensu-plugins-elasticsearch/internal/elasticsearch"
)

const filter = "?filter_path=_nodes,nodes.*.name,nodes.*.jvm.mem.heap_used_percent,nodes.*.jvm.mem.heap_max_in_bytes,nodes.*.jvm.mem.pools.old"

func TestStatsPath(t *testing.T) {
	tests := []struct{ nodes, want string }{
		{"_local", "/_nodes/_local/stats/jvm" + filter},
		{" node-1,node-2 ", "/_nodes/node-1,node-2/stats/jvm" + filter},
		{"data:true", "/_nodes/data:true/stats/jvm" + filter},
		{"a/b?c", "/_nodes/a%2Fb%3Fc/stats/jvm" + filter},
	}
	for _, tt := range tests {
		if got := statsPath(tt.nodes); got != tt.want {
			t.Errorf("statsPath(%q) = %q, want %q", tt.nodes, got, tt.want)
		}
	}
}

func TestValidateThresholds(t *testing.T) {
	tests := []struct {
		warning, critical int
		ok                bool
	}{
		{75, 85, true},
		{90, 90, true},
		{1, 100, true},
		{0, 85, false},
		{75, 101, false},
		{85, 75, false},
	}
	for _, tt := range tests {
		if err := validateThresholds(tt.warning, tt.critical); (err == nil) != tt.ok {
			t.Errorf("validateThresholds(%d, %d) error = %v, want ok %v", tt.warning, tt.critical, err, tt.ok)
		}
	}
}

func TestParseStats(t *testing.T) {
	body := `{"_nodes":{"total":1,"successful":1,"failed":0},"nodes":{"abc":{"name":"node-1","jvm":{"mem":
		{"heap_used_percent":55,"heap_max_in_bytes":1000,"pools":{"old":{"used_in_bytes":420,"max_in_bytes":1000}}}}}}}`
	s, err := parseStats([]byte(body))
	if err != nil {
		t.Fatalf("parseStats() error = %v", err)
	}
	n := s.Nodes["abc"]
	if n.Name != "node-1" || n.JVM.Mem.HeapUsedPercent != 55 || n.JVM.Mem.Pools.Old.Used != 420 {
		t.Errorf("parseStats() = %+v", s)
	}
	if _, err := parseStats([]byte(`{"cluster_name":"prod","status":"green"}`)); !errors.Is(err, errNotStats) {
		t.Errorf("parseStats(health) error = %v, want %v", err, errNotStats)
	}
	if _, err := parseStats([]byte(`<html></html>`)); err == nil {
		t.Error("parseStats(HTML) error = nil, want an invalid JSON error")
	}
}

func node(name string, heap int, oldUsed, oldMax, heapMax int64) Node {
	var n Node
	n.Name = name
	n.JVM.Mem.HeapUsedPercent = heap
	n.JVM.Mem.HeapMax = heapMax
	n.JVM.Mem.Pools.Old.Used = oldUsed
	n.JVM.Mem.Pools.Old.Max = oldMax
	return n
}

func stats(failed int, nodes ...Node) Stats {
	s := Stats{Nodes: map[string]Node{}}
	s.Header = &struct {
		Total  int `json:"total"`
		Failed int `json:"failed"`
	}{len(nodes) + failed, failed}
	for i, n := range nodes {
		s.Nodes[string(rune('a'+i))] = n
	}
	return s
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name  string
		stats Stats
		want  elasticsearch.Result
	}{
		{
			name:  "one node OK",
			stats: stats(0, node("node-1", 55, 420, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateOK, Message: "memory pressure node-1 42% (heap 55%)"},
		},
		{
			name:  "several nodes OK",
			stats: stats(0, node("node-1", 55, 420, 1000, 1000), node("node-2", 60, 600, 1000, 1000), node("node-3", 30, 100, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateOK, Message: "memory pressure below 75% on 3 node(s), highest node-2 60% (heap 60%)"},
		},
		{
			name:  "warning threshold reached",
			stats: stats(0, node("node-1", 80, 750, 1000, 1000), node("node-2", 40, 300, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateWarning, Message: "memory pressure at or above 75% on 1/2 node(s): node-1 75% (heap 80%)"},
		},
		{
			name:  "critical wins, nodes sorted by pressure",
			stats: stats(0, node("node-1", 80, 780, 1000, 1000), node("node-2", 95, 900, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateCritical, Message: "memory pressure at or above 75% on 2/2 node(s): node-2 90% (heap 95%), node-1 78% (heap 80%)"},
		},
		{
			name:  "old generation without maximum uses the heap size",
			stats: stats(0, node("node-1", 90, 860, -1, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateCritical, Message: "memory pressure at or above 75% on 1/1 node(s): node-1 86% (heap 90%)"},
		},
		{
			name:  "node name missing",
			stats: stats(0, node("", 10, 100, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateOK, Message: "memory pressure a 10% (heap 10%)"},
		},
		{
			name:  "pressure unavailable",
			stats: stats(0, node("node-1", 10, 100, 1000, 1000), node("node-2", 0, 0, 0, 0)),
			want:  elasticsearch.Result{State: sensu.CheckStateWarning, Message: "memory pressure below 75% on 1 node(s), highest node-1 10% (heap 10%), memory pressure unavailable on node-2"},
		},
		{
			name:  "failed nodes",
			stats: stats(1, node("node-1", 10, 100, 1000, 1000)),
			want:  elasticsearch.Result{State: sensu.CheckStateWarning, Message: "memory pressure node-1 10% (heap 10%), 1 node(s) did not answer"},
		},
		{
			name:  "no matching node",
			stats: stats(0),
			want:  elasticsearch.Result{State: sensu.CheckStateWarning, Message: `no node matches "_local"`},
		},
		{
			name:  "every node failed",
			stats: stats(2),
			want:  elasticsearch.Result{State: sensu.CheckStateCritical, Message: `2 node(s) matching "_local" did not answer`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evaluate(tt.stats, "_local", 75, 85); got != tt.want {
				t.Errorf("evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
