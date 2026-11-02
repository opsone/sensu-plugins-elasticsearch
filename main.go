package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev2 "github.com/sensu/core/v2"
	"github.com/sensu/sensu-plugin-sdk/sensu"
)

// Config represents the check plugin config.
type Config struct {
	sensu.PluginConfig
	Host     string
	Port     int
	User     string
	Password string
	HTTPS    bool
	Insecure bool
	Timeout  int
}

var (
	plugin = Config{
		PluginConfig: sensu.PluginConfig{
			Name:     "check-elasticsearch-status",
			Short:    "Check the Elasticsearch cluster health (green / yellow / red)",
			Keyspace: "sensu.io/plugins/check-elasticsearch-status/config",
		},
	}

	options = []sensu.ConfigOption{
		&sensu.PluginConfigOption[string]{
			Path:      "host",
			Env:       "CHECK_ELASTICSEARCH_HOST",
			Argument:  "host",
			Shorthand: "H", // -h is reserved for --help
			Usage:     "Elasticsearch host, e.g. 127.0.0.1 (required)",
			Value:     &plugin.Host,
		},
		&sensu.PluginConfigOption[int]{
			Path:      "port",
			Env:       "CHECK_ELASTICSEARCH_PORT",
			Argument:  "port",
			Shorthand: "p",
			Usage:     "Elasticsearch HTTP port, e.g. 9200 (required)",
			Value:     &plugin.Port,
		},
		&sensu.PluginConfigOption[string]{
			Path:      "user",
			Env:       "CHECK_ELASTICSEARCH_USER",
			Argument:  "user",
			Shorthand: "u",
			Usage:     "User for HTTP basic authentication (default: no authentication)",
			Value:     &plugin.User,
		},
		&sensu.PluginConfigOption[string]{
			Path:      "password",
			Env:       "CHECK_ELASTICSEARCH_PASSWORD",
			Argument:  "password",
			Shorthand: "P",
			Secret:    true,
			Usage:     "Password for HTTP basic authentication",
			Value:     &plugin.Password,
		},
		&sensu.PluginConfigOption[bool]{
			Path:      "https",
			Env:       "CHECK_ELASTICSEARCH_HTTPS",
			Argument:  "https",
			Shorthand: "s",
			Usage:     "Connect with HTTPS",
			Value:     &plugin.HTTPS,
		},
		&sensu.PluginConfigOption[bool]{
			Path:      "insecure",
			Env:       "CHECK_ELASTICSEARCH_INSECURE",
			Argument:  "insecure",
			Shorthand: "k",
			Usage:     "Do not verify the TLS certificate (with --https)",
			Value:     &plugin.Insecure,
		},
		&sensu.PluginConfigOption[int]{
			Path:     "timeout",
			Env:      "CHECK_ELASTICSEARCH_TIMEOUT",
			Argument: "timeout",
			Default:  10,
			Usage:    "HTTP request timeout in seconds",
			Value:    &plugin.Timeout,
		},
	}
)

func main() {
	check := sensu.NewGoCheck(&plugin.PluginConfig, options, checkArgs, executeCheck, false)
	check.Execute()
}

func checkArgs(event *corev2.Event) (int, error) {
	switch {
	case plugin.Host == "":
		return sensu.CheckStateUnknown, fmt.Errorf("--host is required")
	case plugin.Port < 1 || plugin.Port > 65535:
		return sensu.CheckStateUnknown, fmt.Errorf("--port is required, between 1 and 65535")
	case plugin.Password != "" && plugin.User == "":
		return sensu.CheckStateUnknown, fmt.Errorf("--password requires --user")
	case plugin.Timeout <= 0:
		return sensu.CheckStateUnknown, fmt.Errorf("--timeout must be greater than 0")
	}
	return sensu.CheckStateOK, nil
}

func executeCheck(event *corev2.Event) (int, error) {
	result := fetch(healthURL(plugin.Host, plugin.Port, plugin.HTTPS))
	fmt.Printf("%s %s: %s\n", plugin.Name, stateLabel(result.State), result.Message)
	return result.State, nil
}

// Result is the outcome of the check.
type Result struct {
	State   int
	Message string
}

func fetch(target string) Result {
	client := &http.Client{
		Timeout: time.Duration(plugin.Timeout) * time.Second,
		// Report redirections instead of following them: they mean a misconfigured endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if plugin.Insecure {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client.Transport = transport
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return Result{sensu.CheckStateUnknown, err.Error()}
	}
	if plugin.User != "" {
		req.SetBasicAuth(plugin.User, plugin.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{sensu.CheckStateCritical, fmt.Sprintf("Elasticsearch is down: %v", describeError(err))}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{sensu.CheckStateCritical, fmt.Sprintf("reading %s: %v", target, err)}
	}
	if r, done := evaluateHTTP(resp.StatusCode, body, target); done {
		return r
	}
	health, err := parseHealth(body)
	if err != nil {
		return Result{sensu.CheckStateWarning, fmt.Sprintf("%s: %v", target, err)}
	}
	return evaluate(health)
}

// healthURL builds the cluster health API URL.
func healthURL(host string, port int, https bool) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	scheme := "http"
	if https {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/_cluster/health"
}

// evaluateHTTP classifies a non-200 status code; done is false when the body must be parsed.
func evaluateHTTP(code int, body []byte, target string) (r Result, done bool) {
	reason := ""
	if e := errorReason(body); e != "" {
		reason = ": " + e
	}
	switch {
	case code == http.StatusOK:
		return Result{}, false
	case code >= 300 && code < 400, code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusNotFound:
		return Result{sensu.CheckStateWarning, fmt.Sprintf("health endpoint misconfigured: HTTP %d on %s%s", code, target, reason)}, true
	default:
		return Result{sensu.CheckStateCritical, fmt.Sprintf("Elasticsearch error: HTTP %d on %s%s", code, target, reason)}, true
	}
}

// errorReason extracts the error type from an Elasticsearch error body, if any.
func errorReason(body []byte) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Type
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
func evaluate(h Health) Result {
	var state int
	switch strings.ToLower(h.Status) {
	case "green":
		state = sensu.CheckStateOK
	case "yellow":
		state = sensu.CheckStateWarning
	case "red":
		state = sensu.CheckStateCritical
	default:
		return Result{sensu.CheckStateWarning, fmt.Sprintf("unknown cluster status %q", h.Status)}
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
	return Result{state, msg}
}

// describeError drops the `Get "<url>":` prefix added by net/http, keeping the URL at the end.
func describeError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("%v (%s)", urlErr.Err, urlErr.URL)
	}
	return err.Error()
}

func stateLabel(state int) string {
	switch state {
	case sensu.CheckStateOK:
		return "OK"
	case sensu.CheckStateWarning:
		return "WARNING"
	case sensu.CheckStateCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}
