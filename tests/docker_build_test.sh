#!/usr/bin/env bash
set -e

# Windows Git Bash 兼容：关闭 MSYS 对容器内路径参数（如 /www/index.html）的转换
if command -v cygpath >/dev/null 2>&1; then
  export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
fi

echo "[test] building docker image..."
docker build -t test-aria2-one:local .
echo "[test] docker build succeeded!"

echo "[test] verifying installed binaries in image..."
docker run --rm test-aria2-one:local /usr/local/bin/aria2c -v | head -n 1
docker run --rm test-aria2-one:local nginx -v
docker run --rm test-aria2-one:local test -f /www/index.html
docker run --rm test-aria2-one:local test -f /www/inject.js
docker run --rm test-aria2-one:local test -f /www/inject.css
docker run --rm test-aria2-one:local test -f /www/mirror.js
docker run --rm test-aria2-one:local grep -q 'src="mirror.js"' /www/index.html
docker run --rm test-aria2-one:local test -x /app/aria2-controller

echo "[test] all image binary checks passed!"
