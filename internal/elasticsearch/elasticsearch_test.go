package elasticsearch

import (
	"testing"

	"github.com/sensu/sensu-plugin-sdk/sensu"
)

func TestURL(t *testing.T) {
	tests := []struct {
		conn Conn
		want string
	}{
		{Conn{Host: "127.0.0.1", Port: 9200}, "http://127.0.0.1:9200/_cluster/health"},
		{Conn{Host: "es.example.com", Port: 9243, HTTPS: true}, "https://es.example.com:9243/_cluster/health"},
		{Conn{Host: "2001:db8::1", Port: 9200}, "http://[2001:db8::1]:9200/_cluster/health"},
		{Conn{Host: "[2001:db8::1]", Port: 9200, HTTPS: true}, "https://[2001:db8::1]:9200/_cluster/health"},
	}
	for _, tt := range tests {
		if got := tt.conn.URL("/_cluster/health"); got != tt.want {
			t.Errorf("URL() with %+v = %q, want %q", tt.conn, got, tt.want)
		}
	}
}

func TestEndpoint(t *testing.T) {
	c := Conn{Host: "127.0.0.1", Port: 9200}
	if got, want := c.Endpoint("/_nodes/_local/stats/jvm?filter_path=nodes"), "http://127.0.0.1:9200/_nodes/_local/stats/jvm"; got != want {
		t.Errorf("Endpoint() = %q, want %q", got, want)
	}
}

func TestValidate(t *testing.T) {
	valid := Conn{Host: "127.0.0.1", Port: 9200, Timeout: 10}
	tests := []struct {
		name    string
		mutate  func(*Conn)
		wantErr bool
	}{
		{"valid", func(*Conn) {}, false},
		{"with credentials", func(c *Conn) { c.User, c.Password = "sensu", "secret" }, false},
		{"no host", func(c *Conn) { c.Host = "" }, true},
		{"no port", func(c *Conn) { c.Port = 0 }, true},
		{"port too high", func(c *Conn) { c.Port = 65536 }, true},
		{"password without user", func(c *Conn) { c.Password = "secret" }, true},
		{"zero timeout", func(c *Conn) { c.Timeout = 0 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.mutate(&c)
			if err := c.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEvaluateHTTP(t *testing.T) {
	const target = "http://127.0.0.1:9200/_cluster/health"
	tests := []struct {
		name string
		code int
		body string
		want *Result
	}{
		{name: "200 is parsed", code: 200},
		{
			name: "401 with security exception",
			code: 401,
			body: `{"error":{"type":"security_exception","reason":"missing authentication credentials"},"status":401}`,
			want: &Result{sensu.CheckStateWarning, "API endpoint misconfigured: HTTP 401 on " + target + ": security_exception"},
		},
		{
			name: "301 redirection",
			code: 301,
			want: &Result{sensu.CheckStateWarning, "API endpoint misconfigured: HTTP 301 on " + target},
		},
		{
			name: "404 without JSON body",
			code: 404,
			body: "not found",
			want: &Result{sensu.CheckStateWarning, "API endpoint misconfigured: HTTP 404 on " + target},
		},
		{
			name: "503 without master",
			code: 503,
			body: `{"error":{"type":"master_not_discovered_exception"},"status":503}`,
			want: &Result{sensu.CheckStateCritical, "Elasticsearch error: HTTP 503 on " + target + ": master_not_discovered_exception"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateHTTP(tt.code, []byte(tt.body), target)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("EvaluateHTTP() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
