#!/usr/bin/env bash
# Runs check-elasticsearch-status against a real Elasticsearch (single node, security and
# HTTPS auto-configured, like a default 8+/9+ install) in Docker.
set -euo pipefail
cd "$(dirname "$0")"

id=$$
image=check-elasticsearch-integration
network=check-elasticsearch-$id
eshost=check-elasticsearch-es-$id
runner=check-elasticsearch-runner-$id
es_image=elasticsearch:9.5.3
password=sensu-secret

docker build -q -t "$image" . >/dev/null
docker network create "$network" >/dev/null
cleanup() {
  docker rm -f "$runner" "$eshost" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$eshost" --network "$network" -m 1g \
  -e discovery.type=single-node -e ELASTIC_PASSWORD="$password" \
  -e ES_JAVA_OPTS="-Xms512m -Xmx512m" -e cluster.routing.allocation.disk.threshold_enabled=false \
  "$es_image" >/dev/null
docker run -d --name "$runner" --network "$network" "$image" >/dev/null

run() { docker exec "$runner" bash -c "$1"; }
es() { docker exec "$runner" curl -sfk -u "elastic:$password" -H 'Content-Type: application/json' "$@" >/dev/null; }

for _ in $(seq 120); do es "https://$eshost:9200/_cluster/health?wait_for_status=green&timeout=1s" && break; sleep 1; done
es "https://$eshost:9200/_cluster/health?wait_for_status=green&timeout=1s"
# User without the monitor privilege, for the 403 case.
es -X POST "https://$eshost:9200/_security/user/nopriv" -d '{"password":"nopriv-secret","roles":[]}'
# Plain HTTP servers: 200 with a page that is not a health response (8080), 404 (8081).
run 'mkdir -p /srv/www/_cluster /srv/empty && echo "<html>hello</html>" > /srv/www/_cluster/health &&
     (runuser -u nobody -- python3 -m http.server -d /srv/www 8080 >/tmp/http.log 2>&1 &) &&
     (runuser -u nobody -- python3 -m http.server -d /srv/empty 8081 >/tmp/http404.log 2>&1 &)'
run 'for _ in $(seq 30); do curl -s -o /dev/null http://127.0.0.1:8081/ && curl -sf -o /dev/null http://127.0.0.1:8080/_cluster/health && exit; sleep 0.2; done'

failures=0
expect() {
  local want=$1 desc=$2; shift 2
  local out rc=0
  # Run as an unprivileged user, like sensu-agent does.
  out=$(docker exec "$runner" runuser -u nobody -- "$@" 2>&1) || rc=$?
  out=$(tail -1 <<<"$out")
  if [[ $rc -eq $want ]]; then
    echo "ok   [$rc] $desc -> $out"
  else
    echo "FAIL [$rc, want $want] $desc -> $out"
    failures=$((failures + 1))
  fi
}

args=(check-elasticsearch-status -H "$eshost" -p 9200 -s -k -u elastic -P "$password")
expect 3 "without host" check-elasticsearch-status -p 9200
expect 3 "without port" check-elasticsearch-status -H "$eshost"
expect 3 "password without user" check-elasticsearch-status -H "$eshost" -p 9200 -P "$password"
expect 0 "green" "${args[@]}"
expect 0 "green, settings from env" env CHECK_ELASTICSEARCH_HOST="$eshost" CHECK_ELASTICSEARCH_PORT=9200 \
  CHECK_ELASTICSEARCH_HTTPS=true CHECK_ELASTICSEARCH_INSECURE=true CHECK_ELASTICSEARCH_USER=elastic \
  CHECK_ELASTICSEARCH_PASSWORD="$password" check-elasticsearch-status
expect 2 "self-signed certificate not trusted" check-elasticsearch-status -H "$eshost" -p 9200 -s -u elastic -P "$password"
expect 2 "plain HTTP on the HTTPS port" check-elasticsearch-status -H "$eshost" -p 9200 -u elastic -P "$password"
expect 1 "no credentials (401)" check-elasticsearch-status -H "$eshost" -p 9200 -s -k
expect 1 "wrong password (401)" "${args[@]}" -P wrong
expect 1 "no monitor privilege (403)" "${args[@]}" -u nopriv -P nopriv-secret
expect 1 "not a health response (200)" check-elasticsearch-status -H 127.0.0.1 -p 8080
expect 1 "not found (404)" check-elasticsearch-status -H 127.0.0.1 -p 8081
expect 2 "connection refused" check-elasticsearch-status -H 127.0.0.1 -p 1
expect 2 "unreachable host (timeout)" check-elasticsearch-status -H 192.0.2.1 -p 9200 --timeout 1

# Yellow: a replica that cannot be allocated on a single node.
es -X PUT "https://$eshost:9200/yellow" -d '{"settings":{"number_of_shards":1,"number_of_replicas":1}}'
expect 1 "yellow (unassigned replica)" "${args[@]}"

# Red: a primary that cannot be allocated anywhere.
es -X PUT "https://$eshost:9200/red?wait_for_active_shards=0" \
  -d '{"settings":{"number_of_shards":1,"number_of_replicas":0,"index.routing.allocation.require._name":"nope"}}'
expect 2 "red (unassigned primary)" "${args[@]}"

exit $failures
