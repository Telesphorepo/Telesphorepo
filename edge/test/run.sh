#!/bin/sh
# Starts the edge Worker for one e2e test (edge/test/Containerfile). The test sets:
#   ORIGIN          the server, e.g. http://repo-1a2b3c4d:8080 (the Worker forwards there)
#   PREFIX          s3.prefix of the server
#   CF_LAT, CF_LON  where the client is: request.cf.latitude and longitude (default London)
#   WORKER_VERSION  the version that the Worker is built as (default dev, like the server image)
set -e

# request.cf comes from this file, not from workers.cloudflare.com, so the bucket choice of the
# Worker is the same on every machine.
cat > /tmp/cf.json <<EOF
{"colo":"LHR","country":"GB","city":"London","continent":"EU","region":"England","timezone":"Europe/London",
 "latitude":"${CF_LAT:-51.5074}","longitude":"${CF_LON:--0.1278}","asn":0,"asOrganization":"",
 "httpProtocol":"HTTP/1.1","requestPriority":"","tlsCipher":"","tlsVersion":"","clientTcpRtt":0,
 "edgeRequestKeepAliveStatus":1}
EOF
export CLOUDFLARE_CF_FETCH_PATH=/tmp/cf.json

set -- --var "ORIGIN:${ORIGIN:?set ORIGIN}" --var "PREFIX:${PREFIX:-}"
if [ -n "${WORKER_VERSION:-}" ]; then
  set -- "$@" --define "TELESPHOREPO_VERSION:\"$WORKER_VERSION\""
fi
exec ./node_modules/.bin/wrangler dev --config test/wrangler.jsonc --local --ip 0.0.0.0 --port 8787 \
  --test-scheduled --persist-to /tmp/state --show-interactive-dev-session=false "$@"
