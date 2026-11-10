// Package elasticsearch holds the connection options and the HTTP client shared by the checks.
package elasticsearch

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

	"github.com/sensu/sensu-plugin-sdk/sensu"
)

// Result is the outcome of a check.
type Result struct {
	State   int
	Message string
}

// Conn holds the connection settings, common to every check.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	HTTPS    bool
	Insecure bool
	Timeout  int
}

// Options returns the connection options, bound to the fields of c.
func (c *Conn) Options() []sensu.ConfigOption {
	return []sensu.ConfigOption{
		&sensu.PluginConfigOption[string]{
			Path:      "host",
			Env:       "CHECK_ELASTICSEARCH_HOST",
			Argument:  "host",
			Shorthand: "H", // -h is reserved for --help
			Usage:     "Elasticsearch host, e.g. 127.0.0.1 (required)",
			Value:     &c.Host,
		},
		&sensu.PluginConfigOption[int]{
			Path:      "port",
			Env:       "CHECK_ELASTICSEARCH_PORT",
			Argument:  "port",
			Shorthand: "p",
			Usage:     "Elasticsearch HTTP port, e.g. 9200 (required)",
			Value:     &c.Port,
		},
		&sensu.PluginConfigOption[string]{
			Path:      "user",
			Env:       "CHECK_ELASTICSEARCH_USER",
			Argument:  "user",
			Shorthand: "u",
			Usage:     "User for HTTP basic authentication (default: no authentication)",
			Value:     &c.User,
		},
		&sensu.PluginConfigOption[string]{
			Path:      "password",
			Env:       "CHECK_ELASTICSEARCH_PASSWORD",
			Argument:  "password",
			Shorthand: "P",
			Secret:    true,
			Usage:     "Password for HTTP basic authentication",
			Value:     &c.Password,
		},
		&sensu.PluginConfigOption[bool]{
			Path:      "https",
			Env:       "CHECK_ELASTICSEARCH_HTTPS",
			Argument:  "https",
			Shorthand: "s",
			Usage:     "Connect with HTTPS",
			Value:     &c.HTTPS,
		},
		&sensu.PluginConfigOption[bool]{
			Path:      "insecure",
			Env:       "CHECK_ELASTICSEARCH_INSECURE",
			Argument:  "insecure",
			Shorthand: "k",
			Usage:     "Do not verify the TLS certificate (with --https)",
			Value:     &c.Insecure,
		},
		&sensu.PluginConfigOption[int]{
			Path:     "timeout",
			Env:      "CHECK_ELASTICSEARCH_TIMEOUT",
			Argument: "timeout",
			Default:  10,
			Usage:    "HTTP request timeout in seconds",
			Value:    &c.Timeout,
		},
	}
}

// Validate checks the connection options.
func (c *Conn) Validate() error {
	switch {
	case c.Host == "":
		return errors.New("--host is required")
	case c.Port < 1 || c.Port > 65535:
		return errors.New("--port is required, between 1 and 65535")
	case c.Password != "" && c.User == "":
		return errors.New("--password requires --user")
	case c.Timeout <= 0:
		return errors.New("--timeout must be greater than 0")
	}
	return nil
}

// URL builds the URL of an API path (starting with /, query string included).
func (c *Conn) URL(path string) string {
	host := strings.TrimSuffix(strings.TrimPrefix(c.Host, "["), "]")
	scheme := "http"
	if c.HTTPS {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(c.Port)) + path
}

// Endpoint is the URL of an API path without its query string, for messages.
func (c *Conn) Endpoint(path string) string {
	u, _, _ := strings.Cut(c.URL(path), "?")
	return u
}

// Get requests an API path and returns the body of a 200 response.
// Any other outcome (connection error, other HTTP code) is returned as a final result.
func (c *Conn) Get(path string) ([]byte, *Result) {
	target, endpoint := c.URL(path), c.Endpoint(path)
	client := &http.Client{
		Timeout: time.Duration(c.Timeout) * time.Second,
		// Report redirections instead of following them: they mean a misconfigured endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if c.Insecure {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client.Transport = transport
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, &Result{sensu.CheckStateUnknown, err.Error()}
	}
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &Result{sensu.CheckStateCritical, fmt.Sprintf("Elasticsearch is down: %v", describeError(err, endpoint))}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, &Result{sensu.CheckStateCritical, fmt.Sprintf("reading %s: %v", endpoint, err)}
	}
	if r := EvaluateHTTP(resp.StatusCode, body, endpoint); r != nil {
		return nil, r
	}
	return body, nil
}

// EvaluateHTTP classifies a non-200 status code; it returns nil for 200, whose body must be parsed.
func EvaluateHTTP(code int, body []byte, target string) *Result {
	reason := ""
	if e := errorReason(body); e != "" {
		reason = ": " + e
	}
	switch {
	case code == http.StatusOK:
		return nil
	case code >= 300 && code < 400, code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusNotFound:
		return &Result{sensu.CheckStateWarning, fmt.Sprintf("API endpoint misconfigured: HTTP %d on %s%s", code, target, reason)}
	default:
		return &Result{sensu.CheckStateCritical, fmt.Sprintf("Elasticsearch error: HTTP %d on %s%s", code, target, reason)}
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

// describeError drops the `Get "<url>":` prefix added by net/http, keeping the endpoint at the end.
func describeError(err error, endpoint string) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("%v (%s)", urlErr.Err, endpoint)
	}
	return err.Error()
}

// Report prints the result in the Sensu check output format and returns its state.
func Report(name string, r Result) (int, error) {
	fmt.Printf("%s %s: %s\n", name, StateLabel(r.State), r.Message)
	return r.State, nil
}

// StateLabel returns the name of a check state.
func StateLabel(state int) string {
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
