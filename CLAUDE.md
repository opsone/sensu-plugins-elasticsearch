# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Sensu Go check of the Elasticsearch cluster health, written in Go with `github.com/sensu/sensu-plugin-sdk`. Published as a Sensu asset (static binary, `linux/amd64` only, see `ARCHS` in the Makefile). Same layout and tooling as `opsone/sensu-plugins-systemd` and `opsone/sensu-plugins-apache` (single binary at the root).

## Common Commands

```bash
make test          # go vet + unit tests
make build         # local binary at the root
make integration   # Docker: Elasticsearch 9 single node (security + HTTPS auto-configured) + Debian runner
make dist          # tests, then tar.gz per arch + sha512 + asset YAML in dist/
make release       # dist + GitHub release of the current tag (gh)
```

Version comes from `git describe --tags` (override with `VERSION=`), injected via ldflags into the SDK `version` package.

## Architecture

```
main.go                # options, HTTP request to /_cluster/health, pure evaluation functions
main_test.go           # table tests of healthURL, evaluateHTTP, parseHealth, evaluate
integration/           # Dockerfile (runner) + run.sh, checks run as `nobody`
scripts/asset-yml.sh   # builds the Sensu asset definition from the checksums file
```

`evaluateHTTP` classifies non-200 codes (3xx/401/403/404 WARNING, others CRITICAL), `evaluate` maps green/yellow/red to OK/WARNING/CRITICAL. Network errors are CRITICAL, invalid args UNKNOWN.

Options are declared with `sensu.PluginConfigOption`, each with a `CHECK_ELASTICSEARCH_*` env var. Host-specific values (host, port) have no default in the binary: the defaults live in the CheckConfig. `-h` is reserved by cobra for `--help` (redefining it panics).
