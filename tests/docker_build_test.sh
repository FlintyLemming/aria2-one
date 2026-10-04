#!/usr/bin/env bash
set -e

echo "[test] building docker image..."
docker build -t test-aria2-one:local .
echo "[test] docker build succeeded!"

echo "[test] verifying installed binaries in image..."
docker run --rm test-aria2-one:local /usr/local/bin/aria2c -v | head -n 1
docker run --rm test-aria2-one:local nginx -v
docker run --rm test-aria2-one:local test -f /www/index.html
docker run --rm test-aria2-one:local test -f /www/inject.js
docker run --rm test-aria2-one:local test -f /www/inject.css
docker run --rm test-aria2-one:local test -x /app/aria2-controller

echo "[test] all image binary checks passed!"
