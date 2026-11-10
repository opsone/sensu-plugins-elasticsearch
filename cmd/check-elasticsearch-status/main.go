package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev2 "github.com/sensu/core/v2"
	"github.com/sensu/sensu-plugin-sdk/sensu"

	"github.com/opsone/sensu-plugins-elasticsearch/internal/elasticsearch"
)

// Config represents the check plugin config.
type Config struct {
	sensu.PluginConfig
	elasticsearch.Conn
}

var plugin = Config{
	PluginConfig: sensu.PluginConfig{
		Name:     "check-elasticsearch-status",
		Short:    "Check the Elasticsearch cluster health (green / yellow / red)",
		Keyspace: "sensu.io/plugins/check-elasticsearch-status/config",
	},
}

const healthPath = "/_cluster/health"

func main() {
	check := sensu.NewGoCheck(&plugin.PluginConfig, plugin.Conn.Options(), checkArgs, executeCheck, false)
	check.Execute()
}

func checkArgs(event *corev2.Event) (int, error) {
	if err := plugin.Conn.Validate(); err != nil {
		return sensu.CheckStateUnknown, err
	}
	return sensu.CheckStateOK, nil
}

func executeCheck(event *corev2.Event) (int, error) {
	return elasticsearch.Report(plugin.Name, fetch())
}

func fetch() elasticsearch.Result {
	body, r := plugin.Conn.Get(healthPath)
	if r != nil {
		return *r
	}
	health, err := parseHealth(body)
	if err != nil {
		return elasticsearch.Result{State: sensu.CheckStateWarning, Message: fmt.Sprintf("%s: %v", plugin.Conn.Endpoint(healthPath), err)}
	}
	return evaluate(health)
}

// Health holds the fields of the cluster health API used by the check.
type Health struct {
	ClusterName        string `json:"cluster_name"`
	Status             string `json:"status"`
	NumberOfNodes      int    `json:"number_of_nodes"`
	ActiveShards       int    `json:"active_shards"`
	RelocatingShards   int    `json:"relocating_shards"`
	InitializingShards int    `json:"initializing_shards"`
	UnassignedShards   int    `json:"unassigned_shards"`
}

var errNotHealth = errors.New("not a cluster health response (no status field)")

func parseHealth(body []byte) (Health, error) {
	var h Health
	if err := json.Unmarshal(body, &h); err != nil {
		return h, fmt.Errorf("invalid JSON: %w", err)
	}
	if h.Status == "" {
		return h, errNotHealth
	}
	return h, nil
}

// evaluate maps the cluster status to a check state.
func evaluate(h Health) elasticsearch.Result {
	var state int
	switch strings.ToLower(h.Status) {
	case "green":
		state = sensu.CheckStateOK
	case "yellow":
		state = sensu.CheckStateWarning
	case "red":
		state = sensu.CheckStateCritical
	default:
		return elasticsearch.Result{State: sensu.CheckStateWarning, Message: fmt.Sprintf("unknown cluster status %q", h.Status)}
	}

	total := h.ActiveShards + h.InitializingShards + h.UnassignedShards
	msg := fmt.Sprintf("cluster %s is %s: %d node(s), %d/%d active shards",
		h.ClusterName, strings.ToLower(h.Status), h.NumberOfNodes, h.ActiveShards, total)
	var extra []string
	if h.InitializingShards > 0 {
		extra = append(extra, fmt.Sprintf("%d initializing", h.InitializingShards))
	}
	if h.RelocatingShards > 0 {
		extra = append(extra, fmt.Sprintf("%d relocating", h.RelocatingShards))
	}
	if h.UnassignedShards > 0 {
		extra = append(extra, fmt.Sprintf("%d unassigned", h.UnassignedShards))
	}
	if len(extra) > 0 {
		msg += ", " + strings.Join(extra, ", ")
	}
	return elasticsearch.Result{State: state, Message: msg}
}
