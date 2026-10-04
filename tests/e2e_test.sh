#!/usr/bin/env bash
set -e

CONTAINER_NAME="aria2-e2e-test-$(date +%s)"
HOST_PORT="28080"
TEST_DL_DIR=$(mktemp -d)
TEST_CFG_DIR=$(mktemp -d)
chmod 777 "$TEST_DL_DIR" "$TEST_CFG_DIR"

cleanup() {
  echo "[e2e] cleaning up test container and temp directories..."
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$TEST_DL_DIR" "$TEST_CFG_DIR"
}
trap cleanup EXIT

echo "[e2e] starting container on port $HOST_PORT..."
docker run -d \
  --name "$CONTAINER_NAME" \
  -p "$HOST_PORT:8080" \
  -e USERNAME=testuser \
  -e PASSWORD=testpass \
  -v "$TEST_DL_DIR:/downloads" \
  -v "$TEST_CFG_DIR:/config" \
  test-aria2-one:local

echo "[e2e] waiting for services to initialize..."
STARTED=false
for i in {1..30}; do
  if curl -s -u testuser:testpass "http://127.0.0.1:$HOST_PORT/jsonrpc" -d '{"jsonrpc":"2.0","id":"ping","method":"aria2.getVersion"}' | grep -q "version"; then
    echo "  Services are up!"
    STARTED=true
    break
  fi
  sleep 1
done

if [ "$STARTED" != "true" ]; then
  echo "FAIL: services did not start within 30 seconds"
  docker logs "$CONTAINER_NAME"
  exit 1
fi

# 1. 验证未经认证的请求返回 401
echo "[e2e] verifying Basic Auth protection..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$HOST_PORT/")
if [ "$HTTP_CODE" != "401" ]; then
  echo "FAIL: Expected 401 Unauthorized, got $HTTP_CODE"
  exit 1
fi
echo "  PASS: 401 returned for unauthenticated request"

# 2. 验证认证通过后前端页面与注入文件
echo "[e2e] verifying authenticated WebUI..."
UI_CONTENT=$(curl -s -u testuser:testpass "http://127.0.0.1:$HOST_PORT/")
if ! echo "$UI_CONTENT" | grep -q "inject.js"; then
  echo "FAIL: inject.js not found in index.html"
  exit 1
fi
echo "  PASS: AriaNg with inject.js delivered"

# 3. 验证直链下载与 Range 响应头
echo "[e2e] creating sample download file in /downloads..."
mkdir -p "$TEST_DL_DIR/test_folder"
echo "sample video streaming content 1234567890" > "$TEST_DL_DIR/test_folder/sample.txt"

DOWNLOAD_RESP=$(curl -s -I -u testuser:testpass "http://127.0.0.1:$HOST_PORT/download/test_folder/sample.txt")
if ! echo "$DOWNLOAD_RESP" | grep -qi "Accept-Ranges: bytes"; then
  echo "FAIL: Accept-Ranges header missing in /download/ response: $DOWNLOAD_RESP"
  exit 1
fi
echo "  PASS: /download/ serves file with Range header"

# 4. 验证任务添加与物理删除联动清理
echo "[e2e] testing task add and physical file removal via controller..."
# 通过 /jsonrpc 添加本地下载任务
ADD_RESP=$(curl -s -u testuser:testpass \
  "http://127.0.0.1:$HOST_PORT/jsonrpc" \
  -d '{"jsonrpc":"2.0","id":"add-test","method":"aria2.addUri","params":[["http://testuser:testpass@127.0.0.1:8080/download/test_folder/sample.txt"],{"dir":"/downloads/e2e_task_dir","out":"downloaded_target.txt"}]}')

GID=$(echo "$ADD_RESP" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
if [ -z "$GID" ]; then
  echo "FAIL: could not get GID from addUri: $ADD_RESP"
  exit 1
fi
echo "  Task added, GID: $GID"

# 等待下载完成
for i in {1..20}; do
  STATUS_RESP=$(curl -s -u testuser:testpass "http://127.0.0.1:$HOST_PORT/jsonrpc" -d "{\"jsonrpc\":\"2.0\",\"id\":\"stat\",\"method\":\"aria2.tellStatus\",\"params\":[\"$GID\"]}")
  if echo "$STATUS_RESP" | grep -q '"status":"complete"'; then
    echo "  Download completed!"
    break
  fi
  sleep 0.5
done

TARGET_FILE="$TEST_DL_DIR/e2e_task_dir/downloaded_target.txt"
if [ ! -f "$TARGET_FILE" ]; then
  echo "FAIL: Expected downloaded file $TARGET_FILE to exist"
  exit 1
fi
echo "  PASS: Target file downloaded successfully on disk"

# 移除任务
RM_RESP=$(curl -s -u testuser:testpass "http://127.0.0.1:$HOST_PORT/jsonrpc" -d "{\"jsonrpc\":\"2.0\",\"id\":\"rm\",\"method\":\"aria2.removeDownloadResult\",\"params\":[\"$GID\"]}")
echo "  removeDownloadResult response: $RM_RESP"

# 轮询验证文件和空文件夹已被物理删除
CLEANED=false
for i in {1..20}; do
  if [ ! -f "$TARGET_FILE" ] && [ ! -d "$TEST_DL_DIR/e2e_task_dir" ]; then
    CLEANED=true
    break
  fi
  sleep 0.2
done

if [ "$CLEANED" != "true" ]; then
  echo "FAIL: Target file or directory was not physically removed by controller"
  exit 1
fi
echo "  PASS: Downloaded file and empty directory were physically cleaned up on remove"

docker exec "$CONTAINER_NAME" /app/aria2-controller -help > /dev/null 2>&1
echo "  PASS: aria2-controller binary runs correctly inside container"

echo "[e2e] All end-to-end integration tests completed successfully!"
