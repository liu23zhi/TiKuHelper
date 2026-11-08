#!/bin/sh
set -eu
cd "$(dirname -- "$0")"
(cd web && npm ci && npm run build)
test -f web/dist/index.html
rm -rf cmd/adapter-service/dist
mv web/dist cmd/adapter-service/dist
