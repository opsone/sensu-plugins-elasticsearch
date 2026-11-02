package main

import (
	"errors"
	"testing"

	"github.com/sensu/sensu-plugin-sdk/sensu"
)

func TestHealthURL(t *testing.T) {
	tests := []struct {
		host  string
		port  int
		https bool
		want  string
	}{
		{"127.0.0.1", 9200, false, "http://127.0.0.1:9200/_cluster/health"},
		{"es.example.com", 9243, true, "https://es.example.com:9243/_cluster/health"},
		{"2001:db8::1", 9200, false, "http://[2001:db8::1]:9200/_cluster/health"},
		{"[2001:db8::1]", 9200, true, "https://[2001:db8::1]:9200/_cluster/health"},
	}
	for _, tt := range tests {
		if got := healthURL(tt.host, tt.port, tt.https); got != tt.want {
			t.Errorf("healthURL(%q, %d, %v) = %q, want %q", tt.host, tt.port, tt.https, got, tt.want)
		}
	}
}

func TestEvaluateHTTP(t *testing.T) {
	const target = "http://127.0.0.1:9200/_cluster/health"
	tests := []struct {
		name  string
		code  int
		body  string
		want  Result
		final bool
	}{
		{name: "200 is parsed", code: 200, final: false},
		{
			name:  "401 with security exception",
			code:  401,
			body:  `{"error":{"type":"security_exception","reason":"missing authentication credentials"},"status":401}`,
			want:  Result{sensu.CheckStateWarning, "health endpoint misconfigured: HTTP 401 on " + target + ": security_exception"},
			final: true,
		},
		{
			name:  "301 redirection",
			code:  301,
			want:  Result{sensu.CheckStateWarning, "health endpoint misconfigured: HTTP 301 on " + target},
			final: true,
		},
		{
			name:  "403 and 404",
			code:  404,
			body:  "not found",
			want:  Result{sensu.CheckStateWarning, "health endpoint misconfigured: HTTP 404 on " + target},
			final: true,
		},
		{
			name:  "503 without master",
			code:  503,
			body:  `{"error":{"type":"master_not_discovered_exception"},"status":503}`,
			want:  Result{sensu.CheckStateCritical, "Elasticsearch error: HTTP 503 on " + target + ": master_not_discovered_exception"},
			final: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, final := evaluateHTTP(tt.code, []byte(tt.body), target)
			if final != tt.final || got != tt.want {
				t.Errorf("evaluateHTTP() = %+v, %v, want %+v, %v", got, final, tt.want, tt.final)
			}
		})
	}
}

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
		want   Result
	}{
		{
			name:   "green",
			health: Health{ClusterName: "prod", Status: "green", NumberOfNodes: 3, ActiveShards: 10},
			want:   Result{sensu.CheckStateOK, "cluster prod is green: 3 node(s), 10/10 active shards"},
		},
		{
			name:   "yellow with unassigned replicas",
			health: Health{ClusterName: "prod", Status: "yellow", NumberOfNodes: 1, ActiveShards: 5, UnassignedShards: 5},
			want:   Result{sensu.CheckStateWarning, "cluster prod is yellow: 1 node(s), 5/10 active shards, 5 unassigned"},
		},
		{
			name: "red, initializing and relocating",
			health: Health{ClusterName: "prod", Status: "RED", NumberOfNodes: 2, ActiveShards: 6,
				InitializingShards: 1, RelocatingShards: 2, UnassignedShards: 1},
			want: Result{sensu.CheckStateCritical, "cluster prod is red: 2 node(s), 6/8 active shards, 1 initializing, 2 relocating, 1 unassigned"},
		},
		{
			name:   "unknown status",
			health: Health{Status: "purple"},
			want:   Result{sensu.CheckStateWarning, `unknown cluster status "purple"`},
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
