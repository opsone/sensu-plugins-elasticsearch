# sensu-plugins-elasticsearch

Sensu Go check reading the Elasticsearch [cluster health API](https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-cluster-health):

- `check-elasticsearch-status`: the cluster answers, and its status is `green`.

Static Go binary (no CGO, no Ruby runtime): one asset for every Linux distribution, `amd64` only.

## Behavior

The check requests `<scheme>://<host>:<port>/_cluster/health`, without following redirections, with optional HTTP basic authentication.

| Situation | State |
|---|---|
| Cluster `green` | OK |
| Cluster `yellow` (all primaries allocated, some replicas not) | WARNING |
| Cluster `red` (some primaries not allocated) | CRITICAL |
| HTTP 3xx, 401, 403, 404: endpoint or credentials misconfigured | WARNING |
| HTTP 200 that is not a health response, or unknown cluster status | WARNING |
| Other HTTP code (e.g. 503 without elected master) | CRITICAL |
| Connection refused, timeout, TLS error | CRITICAL |
| Invalid argument | UNKNOWN |

Output example:

```
check-elasticsearch-status WARNING: cluster prod is yellow: 1 node(s), 5/10 active shards, 5 unassigned
```

Elasticsearch 8+ enables security by default: HTTPS with a self-signed certificate and authentication. Use `--https`, `--insecure` (the generated certificate is not trusted by the system) and a user holding the `monitor` cluster privilege, e.g.:

```
POST /_security/role/sensu
{ "cluster": ["monitor"] }

POST /_security/user/sensu
{ "password": "<password>", "roles": ["sensu"] }
```

## Usage

```
check-elasticsearch-status [flags]
  -H, --host string       Elasticsearch host, e.g. 127.0.0.1 (required)
  -p, --port int          Elasticsearch HTTP port, e.g. 9200 (required)
  -u, --user string       User for HTTP basic authentication (default: no authentication)
  -P, --password string   Password for HTTP basic authentication
  -s, --https             Connect with HTTPS
  -k, --insecure          Do not verify the TLS certificate (with --https)
      --timeout int       HTTP request timeout in seconds (default 10)
```

Every option can also be set with an environment variable, which keeps the password out of the process list:

| Option | Variable |
|---|---|
| `--host` | `CHECK_ELASTICSEARCH_HOST` |
| `--port` | `CHECK_ELASTICSEARCH_PORT` |
| `--user` / `--password` | `CHECK_ELASTICSEARCH_USER` / `CHECK_ELASTICSEARCH_PASSWORD` |
| `--https` / `--insecure` | `CHECK_ELASTICSEARCH_HTTPS` / `CHECK_ELASTICSEARCH_INSECURE` (`true` / `false`) |
| `--timeout` | `CHECK_ELASTICSEARCH_TIMEOUT` |

Examples:

```sh
check-elasticsearch-status -H 127.0.0.1 -p 9200
check-elasticsearch-status -H 127.0.0.1 -p 9200 -s -k -u sensu   # password in CHECK_ELASTICSEARCH_PASSWORD
```

## Check definitions

Every option is passed through `env_vars`, filled per host from entity annotations with token substitution (applied by the agent to the whole check definition, `env_vars` included). One annotation maps to one option.

Naming rule: an annotation is named after its environment variable, lowercased, without the `CHECK_` prefix (`CHECK_ELASTICSEARCH_HOST` → `elasticsearch_host`).

- Optional options get an explicit, non-empty `default`, matching the plugin default. Never use `| default ""`, nor an annotation set to an empty value: Sensu rejects an empty environment variable (`FOO=`), and the agent then silently drops the check, without any event.
- `elasticsearch_user` and `elasticsearch_password` have no default, as no non-empty value means "no authentication". They only appear in the `check-elasticsearch-status-auth` definition, where an entity missing them gets an error event (unmatched token). Clusters without security use `check-elasticsearch-status`, which has no credential line.
- Each host subscribes to one of the two definitions: `elasticsearch` (no authentication) or `elasticsearch-auth`.

Annotations, e.g. in the agent configuration (`/etc/sensu/agent.yml`):

```yaml
annotations:
  elasticsearch_host: 127.0.0.1        # optional, default 127.0.0.1
  elasticsearch_port: "9200"           # optional, default 9200
  elasticsearch_https: "true"          # optional, default false (true on Elasticsearch 8+)
  elasticsearch_insecure: "true"       # optional, default false (true with the self-signed certificate of Elasticsearch 8+)
  elasticsearch_timeout: "5"           # optional, seconds, default 10
  elasticsearch_user: sensu            # required by check-elasticsearch-status-auth
  elasticsearch_password: secret       # required by check-elasticsearch-status-auth
```

### Without authentication

```yaml
---
type: CheckConfig
api_version: core/v2
metadata:
  name: check-elasticsearch-status
spec:
  command: check-elasticsearch-status
  env_vars:
    - CHECK_ELASTICSEARCH_HOST={{ .annotations.elasticsearch_host | default "127.0.0.1" }}
    - CHECK_ELASTICSEARCH_PORT={{ .annotations.elasticsearch_port | default "9200" }}
    - CHECK_ELASTICSEARCH_HTTPS={{ .annotations.elasticsearch_https | default "false" }}
    - CHECK_ELASTICSEARCH_INSECURE={{ .annotations.elasticsearch_insecure | default "false" }}
    - CHECK_ELASTICSEARCH_TIMEOUT={{ .annotations.elasticsearch_timeout | default "10" }}
  runtime_assets:
    - opsone/sensu-plugins-elasticsearch
  interval: 60
  timeout: 30
  publish: true
  subscriptions:
    - elasticsearch
```

### With authentication (Elasticsearch 8+ security)

```yaml
---
type: CheckConfig
api_version: core/v2
metadata:
  name: check-elasticsearch-status-auth
spec:
  command: check-elasticsearch-status
  env_vars:
    - CHECK_ELASTICSEARCH_HOST={{ .annotations.elasticsearch_host | default "127.0.0.1" }}
    - CHECK_ELASTICSEARCH_PORT={{ .annotations.elasticsearch_port | default "9200" }}
    - CHECK_ELASTICSEARCH_HTTPS={{ .annotations.elasticsearch_https | default "false" }}
    - CHECK_ELASTICSEARCH_INSECURE={{ .annotations.elasticsearch_insecure | default "false" }}
    - CHECK_ELASTICSEARCH_TIMEOUT={{ .annotations.elasticsearch_timeout | default "10" }}
    - CHECK_ELASTICSEARCH_USER={{ .annotations.elasticsearch_user }}
    - CHECK_ELASTICSEARCH_PASSWORD={{ .annotations.elasticsearch_password }}
  runtime_assets:
    - opsone/sensu-plugins-elasticsearch
  interval: 60
  timeout: 30
  publish: true
  subscriptions:
    - elasticsearch-auth
```

Entity annotations are visible in the Sensu dashboard and API: to keep the password out of them, use Sensu [secrets](https://docs.sensu.io/sensu-go/latest/operations/manage-secrets/secrets/) instead of `env_vars` for `CHECK_ELASTICSEARCH_PASSWORD`.

## Migrating from the Ruby version (≤ 0.1.0)

- The command loses its `.rb` suffix: `check-elasticsearch-status.rb` → `check-elasticsearch-status`.
- The host shorthand is now `-H` (`-h` is `--help`).
- `--host` and `--port` are required (they defaulted to `127.0.0.1` and `9200`); the defaults now live in the check definition.
- Errors other than the cluster status change state: timeouts and HTTP 5xx are CRITICAL, HTTP 3xx / 401 / 403 / 404 and unreadable responses are WARNING (they were UNKNOWN).
- New: basic authentication (`--user`, `--password`) and HTTPS (`--https`, `--insecure`), needed for Elasticsearch 8+ with security enabled.
- Request timeout of 10 s (`--timeout`), instead of the 60 s Ruby defaults.
- Invalid arguments are UNKNOWN.
- The asset no longer depends on the Ruby runtime asset, nor on the Debian release.

## Build and release

```sh
make test          # go vet + unit tests
make integration   # runs the check against a real Elasticsearch (security and HTTPS enabled) in Docker
```

Release:

```sh
git tag 0.2.0 && git push origin 0.2.0
make release       # version taken from the tag, or forced: make release VERSION=0.2.0
sensuctl create -f dist/opsone-sensu-plugins-elasticsearch_0.2.0.yml
```

`make release` runs `make dist`, which runs the tests then produces in `dist/`:

- one archive per architecture listed in `ARCHS` (`linux/amd64`) in Sensu asset layout (`bin/check-elasticsearch-status`);
- the sha512 checksums file;
- `opsone-sensu-plugins-elasticsearch_<version>.yml`, the asset definition pointing to the GitHub release of the tag.

It then creates the GitHub release of the tag with `gh`, uploads the archive and checksums, and uses the asset definition as release description.

`check-elasticsearch-status version` prints the version, commit and build date.
