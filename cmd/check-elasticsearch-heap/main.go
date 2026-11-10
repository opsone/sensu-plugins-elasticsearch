package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	corev2 "github.com/sensu/core/v2"
	"github.com/sensu/sensu-plugin-sdk/sensu"

	"github.com/opsone/sensu-plugins-elasticsearch/internal/elasticsearch"
)

// Config represents the check plugin config.
type Config struct {
	sensu.PluginConfig
	elasticsearch.Conn
	Nodes    string
	Warning  int
	Critical int
}

var plugin = Config{
	PluginConfig: sensu.PluginConfig{
		Name:     "check-elasticsearch-heap",
		Short:    "Check the JVM memory pressure (old generation usage) of Elasticsearch nodes",
		Keyspace: "sensu.io/plugins/check-elasticsearch-heap/config",
	},
}

func options() []sensu.ConfigOption {
	return append(plugin.Conn.Options(),
		&sensu.PluginConfigOption[string]{
			Path:      "nodes",
			Env:       "CHECK_ELASTICSEARCH_HEAP_NODES",
			Argument:  "nodes",
			Shorthand: "n",
			Default:   "_local",
			Usage:     "Nodes to check, as an Elasticsearch node specification (_local, _all, node names, comma-separated)",
			Value:     &plugin.Nodes,
		},
		&sensu.PluginConfigOption[int]{
			Path:      "warning",
			Env:       "CHECK_ELASTICSEARCH_HEAP_WARNING",
			Argument:  "warning",
			Shorthand: "w",
			Default:   75,
			Usage:     "Memory pressure (%) at or above which the check is WARNING",
			Value:     &plugin.Warning,
		},
		&sensu.PluginConfigOption[int]{
			Path:      "critical",
			Env:       "CHECK_ELASTICSEARCH_HEAP_CRITICAL",
			Argument:  "critical",
			Shorthand: "c",
			Default:   85,
			Usage:     "Memory pressure (%) at or above which the check is CRITICAL",
			Value:     &plugin.Critical,
		},
	)
}

func main() {
	check := sensu.NewGoCheck(&plugin.PluginConfig, options(), checkArgs, executeCheck, false)
	check.Execute()
}

func checkArgs(event *corev2.Event) (int, error) {
	if err := plugin.Conn.Validate(); err != nil {
		return sensu.CheckStateUnknown, err
	}
	if err := validateThresholds(plugin.Warning, plugin.Critical); err != nil {
		return sensu.CheckStateUnknown, err
	}
	if strings.TrimSpace(plugin.Nodes) == "" {
		return sensu.CheckStateUnknown, errors.New("--nodes must not be empty")
	}
	return sensu.CheckStateOK, nil
}

func validateThresholds(warning, critical int) error {
	if warning < 1 || critical > 100 || warning > critical {
		return fmt.Errorf("thresholds must satisfy 1 <= --warning (%d) <= --critical (%d) <= 100", warning, critical)
	}
	return nil
}

func executeCheck(event *corev2.Event) (int, error) {
	return elasticsearch.Report(plugin.Name, fetch())
}

func fetch() elasticsearch.Result {
	path := statsPath(plugin.Nodes)
	body, r := plugin.Conn.Get(path)
	if r != nil {
		return *r
	}
	stats, err := parseStats(body)
	if err != nil {
		return elasticsearch.Result{State: sensu.CheckStateWarning, Message: fmt.Sprintf("%s: %v", plugin.Conn.Endpoint(path), err)}
	}
	return evaluate(stats, plugin.Nodes, plugin.Warning, plugin.Critical)
}

// statsPath builds the node stats API path, restricted to the fields used by the check.
// Commas stay unescaped: they separate the node specifications.
func statsPath(nodes string) string {
	spec := strings.ReplaceAll(url.PathEscape(strings.TrimSpace(nodes)), "%2C", ",")
	return "/_nodes/" + spec + "/stats/jvm?filter_path=" +
		"_nodes,nodes.*.name,nodes.*.jvm.mem.heap_used_percent,nodes.*.jvm.mem.heap_max_in_bytes,nodes.*.jvm.mem.pools.old"
}

// Stats holds the fields of the node stats API used by the check.
type Stats struct {
	Header *struct {
		Total  int `json:"total"`
		Failed int `json:"failed"`
	} `json:"_nodes"`
	Nodes map[string]Node `json:"nodes"`
}

// Node holds the JVM memory statistics of a node.
type Node struct {
	Name string `json:"name"`
	JVM  struct {
		Mem struct {
			HeapUsedPercent int   `json:"heap_used_percent"`
			HeapMax         int64 `json:"heap_max_in_bytes"`
			Pools           struct {
				Old struct {
					Used int64 `json:"used_in_bytes"`
					Max  int64 `json:"max_in_bytes"`
				} `json:"old"`
			} `json:"pools"`
		} `json:"mem"`
	} `json:"jvm"`
}

var errNotStats = errors.New("not a node stats response (no _nodes field)")

func parseStats(body []byte) (Stats, error) {
	var s Stats
	if err := json.Unmarshal(body, &s); err != nil {
		return s, fmt.Errorf("invalid JSON: %w", err)
	}
	if s.Header == nil {
		return s, errNotStats
	}
	return s, nil
}

// pressure returns the old generation usage in percent, as Elastic Cloud computes the
// JVM memory pressure. The heap size stands in for an old generation without maximum.
func (n Node) pressure() (float64, bool) {
	old := n.JVM.Mem.Pools.Old
	limit := old.Max
	if limit <= 0 {
		limit = n.JVM.Mem.HeapMax
	}
	if limit <= 0 {
		return 0, false
	}
	return float64(old.Used) * 100 / float64(limit), true
}

type nodePressure struct {
	name     string
	pressure float64
	heap     int
}

func (p nodePressure) String() string {
	return fmt.Sprintf("%s %.0f%% (heap %d%%)", p.name, p.pressure, p.heap)
}

// evaluate compares the memory pressure of every node with the thresholds: the worst node
// sets the state.
func evaluate(s Stats, nodes string, warning, critical int) elasticsearch.Result {
	if len(s.Nodes) == 0 {
		if s.Header.Failed > 0 {
			return elasticsearch.Result{State: sensu.CheckStateCritical,
				Message: fmt.Sprintf("%d node(s) matching %q did not answer", s.Header.Failed, nodes)}
		}
		return elasticsearch.Result{State: sensu.CheckStateWarning, Message: fmt.Sprintf("no node matches %q", nodes)}
	}

	var all, over []nodePressure
	var unavailable []string
	state := sensu.CheckStateOK
	for id, n := range s.Nodes {
		name := n.Name
		if name == "" {
			name = id
		}
		p, ok := n.pressure()
		if !ok {
			unavailable = append(unavailable, name)
			state = max(state, sensu.CheckStateWarning)
			continue
		}
		np := nodePressure{name, p, n.JVM.Mem.HeapUsedPercent}
		all = append(all, np)
		switch {
		case p >= float64(critical):
			state = sensu.CheckStateCritical
		case p >= float64(warning):
			state = max(state, sensu.CheckStateWarning)
		default:
			continue
		}
		over = append(over, np)
	}
	byPressure := func(l []nodePressure) {
		sort.Slice(l, func(i, j int) bool {
			if l[i].pressure != l[j].pressure {
				return l[i].pressure > l[j].pressure
			}
			return l[i].name < l[j].name
		})
	}
	byPressure(all)
	byPressure(over)
	sort.Strings(unavailable)

	var msg string
	switch {
	case len(over) > 0:
		parts := make([]string, len(over))
		for i, p := range over {
			parts[i] = p.String()
		}
		msg = fmt.Sprintf("memory pressure at or above %d%% on %d/%d node(s): %s",
			warning, len(over), len(s.Nodes), strings.Join(parts, ", "))
	case len(all) == 1 && len(s.Nodes) == 1:
		msg = "memory pressure " + all[0].String()
	case len(all) > 0:
		msg = fmt.Sprintf("memory pressure below %d%% on %d node(s), highest %s", warning, len(all), all[0])
	}
	var extra []string
	if msg != "" {
		extra = append(extra, msg)
	}
	if len(unavailable) > 0 {
		extra = append(extra, "memory pressure unavailable on "+strings.Join(unavailable, ", "))
	}
	if s.Header.Failed > 0 {
		state = max(state, sensu.CheckStateWarning)
		extra = append(extra, fmt.Sprintf("%d node(s) did not answer", s.Header.Failed))
	}
	return elasticsearch.Result{State: state, Message: strings.Join(extra, ", ")}
}
