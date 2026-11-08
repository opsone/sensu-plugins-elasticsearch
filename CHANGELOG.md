# Changelog

## [0.2.1] - 2026-11-08

### Fixed
- README: check definitions no longer use `| default ""`. Sensu rejects an empty environment variable, and the agent silently drops the check on a host missing the annotation. Explicit defaults instead (`false`, `false`, `10`); credentials move to a dedicated `check-elasticsearch-status-auth` definition (subscription `elasticsearch-auth`) where `elasticsearch_user` and `elasticsearch_password` are required.

## [0.2.0] - 2026-11-02

### Changed
- Rewritten in Go with the Sensu plugin SDK: static binary for `linux/amd64`, no Ruby runtime asset.
- Command renamed without the `.rb` suffix (`check-elasticsearch-status`).
- The `--host` shorthand is `-H` (`-h` is `--help`).
- `--host` and `--port` are required, no more `127.0.0.1` and `9200` defaults in the plugin. Set them in the check definition.
- Timeouts and HTTP 5xx are CRITICAL; HTTP 3xx, 401, 403, 404, responses that are not a cluster health and unknown statuses are WARNING (all were UNKNOWN).
- Invalid arguments are UNKNOWN.

### Added
- HTTP basic authentication (`--user`, `--password`) and HTTPS (`--https`, `--insecure`), for Elasticsearch 8+ with security enabled.
- Cluster name, node count and shard allocation in the output.
- `--timeout` (default 10 s).
- Every option can be set through an environment variable (`CHECK_ELASTICSEARCH_*`).

## [0.1.0]

### Added
- `check-elasticsearch-status.rb`.
