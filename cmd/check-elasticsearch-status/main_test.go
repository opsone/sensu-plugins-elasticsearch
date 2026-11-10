package main

import (
	"errors"
	"testing"

	"github.com/sensu/sensu-plugin-sdk/sensu"

	"github.com/opsone/sensu-plugins-elasticsearch/internal/elasticsearch"
)

func TestParseHealth(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    Health
		wantErr error
	}{
		{
			name: "health response",
			body: `{"cluster_name":"prod","status":"yellow","number_of_nodes":1,"active_shards":3,"unassigned_shards":1}`,
			want: Health{ClusterName: "prod", Status: "yellow", NumberOfNodes: 1, ActiveShards: 3, UnassignedShards: 1},
		},
		{name: "other JSON document", body: `{"name":"node-1","tagline":"You Know, for Search"}`, wantErr: errNotHealth},
		{name: "HTML page", body: `<html></html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHealth([]byte(tt.body))
			switch {
			case tt.name == "HTML page":
				if err == nil {
					t.Error("parseHealth() error = nil, want an invalid JSON error")
				}
			case !errors.Is(err, tt.wantErr):
				t.Errorf("parseHealth() error = %v, want %v", err, tt.wantErr)
			case got != tt.want:
				t.Errorf("parseHealth() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name   string
		health Health
		want   elasticsearch.Result
	}{
		{
			name:   "green",
			health: Health{ClusterName: "prod", Status: "green", NumberOfNodes: 3, ActiveShards: 10},
			want:   elasticsearch.Result{State: sensu.CheckStateOK, Message: "cluster prod is green: 3 node(s), 10/10 active shards"},
		},
		{
			name:   "yellow with unassigned replicas",
			health: Health{ClusterName: "prod", Status: "yellow", NumberOfNodes: 1, ActiveShards: 5, UnassignedShards: 5},
			want:   elasticsearch.Result{State: sensu.CheckStateWarning, Message: "cluster prod is yellow: 1 node(s), 5/10 active shards, 5 unassigned"},
		},
		{
			name: "red, initializing and relocating",
			health: Health{ClusterName: "prod", Status: "RED", NumberOfNodes: 2, ActiveShards: 6,
				InitializingShards: 1, RelocatingShards: 2, UnassignedShards: 1},
			want: elasticsearch.Result{State: sensu.CheckStateCritical, Message: "cluster prod is red: 2 node(s), 6/8 active shards, 1 initializing, 2 relocating, 1 unassigned"},
		},
		{
			name:   "unknown status",
			health: Health{Status: "purple"},
			want:   elasticsearch.Result{State: sensu.CheckStateWarning, Message: `unknown cluster status "purple"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evaluate(tt.health); got != tt.want {
				t.Errorf("evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
