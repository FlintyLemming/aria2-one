#!/usr/bin/env bash
set -e

CONTAINER_NAME="aria2-e2e-test-$(date +%s)"
HOST_PORT="28080"
TEST_DL_DIR=$(mktemp -d)
TEST_CFG_DIR=$(mktemp -d)
chmod 777 "$TEST_DL_DIR" "$TEST_CFG_DIR"

# Windows Git Bash 兼容：docker.exe 需要 Windows 风格挂载路径，并关闭 MSYS 参数路径转换
if command -v cygpath >/dev/null 2>&1; then
  DOCKER_BIN=$(command -v docker)
  docker() { MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' "$DOCKER_BIN" "$@"; }
  DOCKER_DL_DIR=$(cygpath -m "$TEST_DL_DIR")
  DOCKER_CFG_DIR=$(cygpath -m "$TEST_CFG_DIR")
else
  DOCKER_DL_DIR="$TEST_DL_DIR"
  DOCKER_CFG_DIR="$TEST_CFG_DIR"
fi

cleanup() {
  echo "[e2e] cleaning up test container and temp directories..."
  # 容器会把挂载目录 chown 给 abc，先放开权限，保证宿主机能删除容器写入的文件
  docker exec "$CONTAINER_NAME" chmod -R a+rwX /downloads /config >/dev/null 2>&1 || true
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$TEST_DL_DIR" "$TEST_CFG_DIR" || true
}
trap cleanup EXIT

# 准备离线 GitHub Release 镜像测试所需的伪造 API 响应与资产文件（由容器内 Nginx 的 /download/ 提供）
FAKE_ASSET_DIR="$TEST_DL_DIR/fake-assets"
FAKE_RELEASE_DIR="$TEST_DL_DIR/fake-api/repos/e2e-owner/e2e-repo/releases"
FAKE_ASSET_URL="http://testuser:testpass@127.0.0.1:8080/download/fake-assets"
mkdir -p "$FAKE_ASSET_DIR" "$FAKE_RELEASE_DIR"
# linux 资产需大于 16 KiB：aria2 continue=true 只会以 16 KiB 块为单位"续传"已有文件，过小的旧文件无法暴露重新上传后的拼接损坏
awk 'BEGIN { for (i = 1; i <= 1200; i++) printf "aria2-one e2e linux asset v1 line %05d\n", i }' > "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz"
printf 'aria2-one e2e windows asset abcdefghij\n' > "$FAKE_ASSET_DIR/e2e-app-windows.zip"
LINUX_ASSET_SIZE=$(wc -c < "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz" | tr -d ' ')
WINDOWS_ASSET_SIZE=$(wc -c < "$FAKE_ASSET_DIR/e2e-app-windows.zip" | tr -d ' ')
# 按当前 LINUX_ASSET_SIZE / WINDOWS_ASSET_SIZE 写出伪造的 releases/latest（重新上传测试会再次调用；先写临时文件再 mv 替换）
write_fake_release() {
  cat > "$FAKE_RELEASE_DIR/.latest.new" <<EOF
{
  "tag_name": "v1.0.0",
  "name": "E2E Release v1.0.0",
  "html_url": "https://github.com/e2e-owner/e2e-repo/releases/tag/v1.0.0",
  "published_at": "2026-10-01T12:00:00Z",
  "draft": false,
  "prerelease": false,
  "body": "## What's Changed\n- e2e",
  "assets": [
    {
      "name": "e2e-app-linux.tar.gz",
      "size": $LINUX_ASSET_SIZE,
      "browser_download_url": "$FAKE_ASSET_URL/e2e-app-linux.tar.gz"
    },
    {
      "name": "e2e-app-windows.zip",
      "size": $WINDOWS_ASSET_SIZE,
      "browser_download_url": "$FAKE_ASSET_URL/e2e-app-windows.zip"
    }
  ]
}
EOF
  mv -f "$FAKE_RELEASE_DIR/.latest.new" "$FAKE_RELEASE_DIR/latest"
}
write_fake_release
chmod -R a+rX "$TEST_DL_DIR/fake-assets" "$TEST_DL_DIR/fake-api"

echo "[e2e] starting container on port $HOST_PORT..."
docker run -d \
  --name "$CONTAINER_NAME" \
  -p "$HOST_PORT:8080" \
  -e USERNAME=testuser \
  -e PASSWORD=testpass \
  -e GITHUB_API_URL=http://testuser:testpass@127.0.0.1:8080/download/fake-api \
  -v "$DOCKER_DL_DIR:/downloads" \
  -v "$DOCKER_CFG_DIR:/config" \
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
if ! echo "$UI_CONTENT" | grep -q "mirror.js"; then
  echo "FAIL: mirror.js not found in index.html"
  exit 1
fi
echo "  PASS: AriaNg with mirror.js delivered"

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

# 5. GitHub Release 镜像：API 鉴权 / 校验 / CRUD 与完全离线的同步链路
BASE_URL="http://127.0.0.1:$HOST_PORT"
MIRROR_FILE_SUFFIX="/github-mirror/E2E/v1.0.0/e2e-app-linux.tar.gz"
MIRROR_VER_DIR="$TEST_DL_DIR/github-mirror/E2E/v1.0.0"
API_CODE=""
API_BODY=""

fail() {
  echo "FAIL: $*"
  echo "---- container logs (tail) ----"
  docker logs --tail 80 "$CONTAINER_NAME" 2>&1 || true
  exit 1
}

# 用法: api METHOD PATH [BODY [CONTENT_TYPE]]，结果写入 API_CODE / API_BODY
api() {
  local out
  if [ $# -ge 3 ]; then
    out=$(curl -s -u testuser:testpass -X "$1" -H "Content-Type: ${4:-application/json}" --data-binary "$3" -w '\n%{http_code}' "$BASE_URL$2") || true
  else
    out=$(curl -s -u testuser:testpass -X "$1" -w '\n%{http_code}' "$BASE_URL$2") || true
  fi
  API_CODE=${out##*$'\n'}
  API_BODY=${out%$'\n'*}
}

# 宿主机可能没有 jq，借用容器内的 jq 解析 JSON；用法: json_get JSON FILTER
json_get() {
  printf '%s' "$1" | docker exec -i "$CONTAINER_NAME" jq -r "$2" 2>/dev/null | tr -d '\r' || true
}

echo "[e2e] verifying /api/mirrors auth and request validation..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/mirrors")
[ "$HTTP_CODE" = "401" ] || fail "Expected 401 for unauthenticated /api/mirrors, got $HTTP_CODE"
echo "  PASS: /api/mirrors is protected by Basic Auth"

api GET /api/mirrors
[ "$API_CODE" = "200" ] || fail "GET /api/mirrors expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.mirrors | type')" = "array" ] || fail "GET /api/mirrors has no mirrors array: $API_BODY"
[ "$(json_get "$API_BODY" '.downloadDir')" = "/downloads" ] || fail "GET /api/mirrors downloadDir mismatch: $API_BODY"
echo "  PASS: GET /api/mirrors returns JSON mirror list"

api POST /api/mirrors '{"repo":"e2e-owner/e2e-repo"}' 'text/plain'
[ "$API_CODE" = "415" ] || fail "POST /api/mirrors with text/plain expected 415, got $API_CODE: $API_BODY"
echo "  PASS: non-JSON Content-Type rejected with 415"

api POST /api/mirrors '{"name":"bad","repo":"not a valid repo!","folder":"","fileTypes":[],"regex":"","enabled":false,"saveChangelog":true,"includePrerelease":false,"keepVersions":0}'
[ "$API_CODE" = "400" ] || fail "POST invalid repo expected 400, got $API_CODE: $API_BODY"
[ -n "$(json_get "$API_BODY" '.error // empty')" ] || fail "400 response has no error message: $API_BODY"
echo "  PASS: invalid repo rejected with 400"

api POST /api/mirrors '{"name":"bad-regex","repo":"e2e-owner/e2e-repo","folder":"BadRegex","fileTypes":[],"regex":"(","enabled":false,"saveChangelog":true,"includePrerelease":false,"keepVersions":0}'
[ "$API_CODE" = "400" ] || fail "POST invalid regex expected 400, got $API_CODE: $API_BODY"
echo "  PASS: invalid regex rejected with 400"

echo "[e2e] creating mirror and syncing from offline fake GitHub API..."
api POST /api/mirrors '{"name":"E2E","repo":"e2e-owner/e2e-repo","folder":"E2E","fileTypes":["tar.gz"],"regex":"","enabled":true,"saveChangelog":true,"includePrerelease":false,"keepVersions":0}'
[ "$API_CODE" = "201" ] || fail "POST /api/mirrors expected 201, got $API_CODE: $API_BODY"
MIRROR_ID=$(json_get "$API_BODY" '.id // empty')
[ -n "$MIRROR_ID" ] || fail "created mirror has no id: $API_BODY"
echo "  PASS: mirror created, id: $MIRROR_ID"

api POST /api/mirrors '{"name":"dup","repo":"e2e-owner/e2e-repo","folder":"e2e","fileTypes":[],"regex":"","enabled":false,"saveChangelog":true,"includePrerelease":false,"keepVersions":0}'
[ "$API_CODE" = "400" ] || fail "POST duplicate folder (case-insensitive) expected 400, got $API_CODE: $API_BODY"
echo "  PASS: duplicate folder rejected with 400"

api POST /api/mirrors '{"name":"","repo":"https://github.com/e2e-owner/e2e-repo.git","folder":"E2E-URL","fileTypes":[],"regex":"","enabled":false,"saveChangelog":true,"includePrerelease":false,"keepVersions":0}'
[ "$API_CODE" = "201" ] || fail "POST repo URL expected 201, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.repo')" = "e2e-owner/e2e-repo" ] || fail "repo URL was not normalized to owner/name: $API_BODY"
URL_MIRROR_ID=$(json_get "$API_BODY" '.id // empty')
api DELETE "/api/mirrors/$URL_MIRROR_ID"
[ "$API_CODE" = "200" ] || fail "DELETE /api/mirrors/$URL_MIRROR_ID expected 200, got $API_CODE: $API_BODY"
echo "  PASS: GitHub URL normalized to owner/name"

api POST "/api/mirrors/$MIRROR_ID/sync" '{}'
[ "$API_CODE" = "202" ] || fail "POST /api/mirrors/$MIRROR_ID/sync expected 202, got $API_CODE: $API_BODY"
echo "  PASS: manual sync accepted with 202"

MIRROR_STATUS=""
DEADLINE=$((SECONDS + 60))
while [ "$SECONDS" -lt "$DEADLINE" ]; do
  api GET /api/mirrors
  MIRROR_STATUS=$(json_get "$API_BODY" ".mirrors[] | select(.id == \"$MIRROR_ID\") | .state.status")
  if [ "$MIRROR_STATUS" = "synced" ]; then
    break
  fi
  if [ "$MIRROR_STATUS" = "error" ]; then
    fail "mirror sync failed: $(json_get "$API_BODY" ".mirrors[] | select(.id == \"$MIRROR_ID\") | .state.lastError")"
  fi
  sleep 1
done
[ "$MIRROR_STATUS" = "synced" ] || fail "mirror did not reach synced within 60s (last status: '$MIRROR_STATUS'): $API_BODY"
MIRROR_VIEW=$(json_get "$API_BODY" ".mirrors[] | select(.id == \"$MIRROR_ID\")")
[ "$(json_get "$MIRROR_VIEW" '.state.latestTag')" = "v1.0.0" ] || fail "state.latestTag mismatch: $MIRROR_VIEW"
[ "$(json_get "$MIRROR_VIEW" '[.state.files[] | .name + ":" + .status] | join(",")')" = "e2e-app-linux.tar.gz:done" ] || fail "state.files should only contain the filtered asset as done: $MIRROR_VIEW"
echo "  PASS: mirror reached synced state"

[ -f "$MIRROR_VER_DIR/e2e-app-linux.tar.gz" ] || fail "mirrored asset $MIRROR_VER_DIR/e2e-app-linux.tar.gz does not exist"
[ "$(cat "$MIRROR_VER_DIR/e2e-app-linux.tar.gz")" = "$(cat "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz")" ] || fail "mirrored asset content differs from source"
[ ! -e "$MIRROR_VER_DIR/e2e-app-linux.tar.gz.aria2" ] || fail "aria2 control file left next to mirrored asset"
[ ! -e "$MIRROR_VER_DIR/e2e-app-windows.zip" ] || fail "asset filtered out by fileTypes was downloaded"
[ -f "$MIRROR_VER_DIR/CHANGELOG.md" ] || fail "CHANGELOG.md was not written"
docker exec "$CONTAINER_NAME" cat /downloads/github-mirror/E2E/v1.0.0/CHANGELOG.md | grep -q "What's Changed" || fail "CHANGELOG.md does not contain release body"
echo "  PASS: asset mirrored to disk with identical content, CHANGELOG written, filtered asset skipped"

api GET "/api/mirrors/$MIRROR_ID/versions"
[ "$API_CODE" = "200" ] || fail "GET versions expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" 'any(.versions[]; .tag == "v1.0.0")')" = "true" ] || fail "versions does not list v1.0.0: $API_BODY"
[ "$(json_get "$API_BODY" '.versions[] | select(.tag == "v1.0.0") | .latest')" = "true" ] || fail "versions does not mark v1.0.0 as latest: $API_BODY"
[ "$(json_get "$API_BODY" '.versions[] | select(.tag == "v1.0.0") | .files[] | select(.name == "CHANGELOG.md") | .name')" = "CHANGELOG.md" ] || fail "versions does not list CHANGELOG.md: $API_BODY"
ASSET_URL=$(json_get "$API_BODY" '.versions[] | select(.tag == "v1.0.0") | .files[] | select(.name == "e2e-app-linux.tar.gz") | .url')
[ "$ASSET_URL" = "/download/github-mirror/E2E/v1.0.0/e2e-app-linux.tar.gz" ] || fail "unexpected direct link '$ASSET_URL': $API_BODY"
DIRECT_CONTENT=$(curl -s -u testuser:testpass "$BASE_URL$ASSET_URL")
[ "$DIRECT_CONTENT" = "$(cat "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz")" ] || fail "direct link $ASSET_URL did not serve the mirrored asset"
echo "  PASS: versions API lists v1.0.0 (latest) with working direct links"

# 同名资产在同一 tag 下被重新上传（内容不同且更大）：必须完整重下，不能把旧文件当作前缀续传
echo "[e2e] verifying a re-uploaded asset (same name, same tag) is downloaded afresh..."
OLD_LINUX_ASSET_SIZE=$LINUX_ASSET_SIZE
awk 'BEGIN { for (i = 1; i <= 3000; i++) printf "re-uploaded e2e linux asset v2 line %05d\n", i }' > "$FAKE_ASSET_DIR/.e2e-app-linux.tar.gz.new"
mv -f "$FAKE_ASSET_DIR/.e2e-app-linux.tar.gz.new" "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz"
LINUX_ASSET_SIZE=$(wc -c < "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz" | tr -d ' ')
[ "$LINUX_ASSET_SIZE" -gt "$OLD_LINUX_ASSET_SIZE" ] || fail "re-uploaded fixture ($LINUX_ASSET_SIZE bytes) is not larger than the old one ($OLD_LINUX_ASSET_SIZE bytes)"
write_fake_release
chmod -R a+rX "$TEST_DL_DIR/fake-assets" "$TEST_DL_DIR/fake-api"

api POST "/api/mirrors/$MIRROR_ID/sync" '{}'
[ "$API_CODE" = "202" ] || fail "POST /api/mirrors/$MIRROR_ID/sync (re-upload) expected 202, got $API_CODE: $API_BODY"
RESYNC_STATE=""
DEADLINE=$((SECONDS + 60))
while [ "$SECONDS" -lt "$DEADLINE" ]; do
  api GET /api/mirrors
  # 形如 "synced:<size>:<status>"，size 来自新的 releases/latest，可区分本次同步与旧状态
  RESYNC_STATE=$(json_get "$API_BODY" ".mirrors[] | select(.id == \"$MIRROR_ID\") | .state | .status + \":\" + ([.files[]? | select(.name == \"e2e-app-linux.tar.gz\") | (.size | tostring) + \":\" + .status] | join(\",\"))")
  if [ "$RESYNC_STATE" = "synced:$LINUX_ASSET_SIZE:done" ]; then
    break
  fi
  case "$RESYNC_STATE" in
    error:*|incomplete:"$LINUX_ASSET_SIZE":*)
      fail "re-uploaded asset sync failed ($RESYNC_STATE): $(json_get "$API_BODY" ".mirrors[] | select(.id == \"$MIRROR_ID\") | .state.lastError")";;
  esac
  sleep 1
done
[ "$RESYNC_STATE" = "synced:$LINUX_ASSET_SIZE:done" ] || fail "mirror did not re-sync the re-uploaded asset within 60s (last state: '$RESYNC_STATE'): $API_BODY"
[ ! -e "$MIRROR_VER_DIR/e2e-app-linux.tar.gz.aria2" ] || fail "aria2 control file left next to re-downloaded asset"
cmp "$MIRROR_VER_DIR/e2e-app-linux.tar.gz" "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz" || fail "re-downloaded asset is not byte-identical to the re-uploaded asset (stale prefix resumed?)"
echo "  PASS: re-uploaded asset fully re-downloaded and byte-identical to the new source"

api GET "/api/mirrors/$MIRROR_ID/versions"
[ "$API_CODE" = "200" ] || fail "GET versions after re-upload expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.versions[] | select(.tag == "v1.0.0") | .latest')" = "true" ] || fail "versions does not mark v1.0.0 as latest after re-upload: $API_BODY"
[ "$(json_get "$API_BODY" '.versions[] | select(.tag == "v1.0.0") | .files[] | select(.name == "e2e-app-linux.tar.gz") | (.size | tostring) + ":" + ((.partial // false) | tostring)')" = "$LINUX_ASSET_SIZE:false" ] || fail "versions should list the re-uploaded asset with its new size and not partial: $API_BODY"
curl -s -u testuser:testpass "$BASE_URL$ASSET_URL" | cmp - "$FAKE_ASSET_DIR/e2e-app-linux.tar.gz" || fail "direct link $ASSET_URL did not serve the re-downloaded asset"
echo "  PASS: versions API reports the new size and the direct link serves the new content"

echo "[e2e] verifying mirror files are protected from task-removal cleanup..."
STOPPED_RESP=$(curl -s -u testuser:testpass "$BASE_URL/jsonrpc" -d '{"jsonrpc":"2.0","id":"stopped","method":"aria2.tellStopped","params":[0,1000,["gid","status","files"]]}')
MIRROR_GID=$(json_get "$STOPPED_RESP" "[.result[]? | select(.status == \"complete\") | select(any(.files[]?; .path | endswith(\"$MIRROR_FILE_SUFFIX\"))) | .gid] | first // empty")
[ -n "$MIRROR_GID" ] || fail "completed aria2 task for mirrored asset not found via tellStopped: $STOPPED_RESP"
echo "  Mirror task found, GID: $MIRROR_GID"
RM_RESP=$(curl -s -u testuser:testpass "$BASE_URL/jsonrpc" -d "{\"jsonrpc\":\"2.0\",\"id\":\"rm-mirror\",\"method\":\"aria2.removeDownloadResult\",\"params\":[\"$MIRROR_GID\"]}")
echo "$RM_RESP" | grep -q '"result":"OK"' || fail "removeDownloadResult for mirror task failed: $RM_RESP"
sleep 2
[ -f "$MIRROR_VER_DIR/e2e-app-linux.tar.gz" ] || fail "mirrored asset was physically deleted together with its aria2 task"
echo "  PASS: mirrored asset survives aria2.removeDownloadResult"

echo "[e2e] deleting mirror together with its files..."
api DELETE "/api/mirrors/$MIRROR_ID?deleteFiles=true"
[ "$API_CODE" = "200" ] || fail "DELETE mirror expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.ok')" = "true" ] || fail "DELETE mirror response is not {\"ok\":true}: $API_BODY"
REMOVED=false
for i in {1..10}; do
  if [ ! -e "$TEST_DL_DIR/github-mirror/E2E" ]; then
    REMOVED=true
    break
  fi
  sleep 0.3
done
[ "$REMOVED" = "true" ] || fail "mirror folder $TEST_DL_DIR/github-mirror/E2E still exists after deleteFiles=true"
api GET /api/mirrors
[ "$(json_get "$API_BODY" "[.mirrors[] | select(.id == \"$MIRROR_ID\")] | length")" = "0" ] || fail "deleted mirror still listed: $API_BODY"
api DELETE "/api/mirrors/$MIRROR_ID"
[ "$API_CODE" = "404" ] || fail "DELETE unknown mirror expected 404, got $API_CODE: $API_BODY"
echo "  PASS: mirror and its folder deleted, unknown id returns 404"

# Token 会以 Bearer 头发给 GITHUB_API_URL 并覆盖 URL 中的 Basic Auth，所以放在同步测试之后
echo "[e2e] verifying mirror settings and GitHub token masking..."
api GET /api/mirror-settings
[ "$API_CODE" = "200" ] || fail "GET /api/mirror-settings expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.rootDir')" = "github-mirror" ] || fail "default rootDir mismatch: $API_BODY"
[ "$(json_get "$API_BODY" '.githubTokenSet')" = "false" ] || fail "githubTokenSet should be false by default: $API_BODY"

api PUT /api/mirror-settings '{"rootDir":"github-mirror","intervalMinutes":5,"proxy":""}'
[ "$API_CODE" = "400" ] || fail "PUT settings with intervalMinutes=5 expected 400, got $API_CODE: $API_BODY"

api PUT /api/mirror-settings '{"rootDir":"github-mirror","intervalMinutes":360,"proxy":"","githubToken":"e2e-dummy-token"}'
[ "$API_CODE" = "200" ] || fail "PUT /api/mirror-settings expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.githubTokenSet')" = "true" ] || fail "githubTokenSet should be true after setting token: $API_BODY"
case "$API_BODY" in *e2e-dummy-token*) fail "token leaked in PUT /api/mirror-settings response";; esac

api GET /api/mirror-settings
[ "$(json_get "$API_BODY" '.githubTokenSet')" = "true" ] || fail "GET settings githubTokenSet should be true: $API_BODY"
case "$API_BODY" in *e2e-dummy-token*) fail "token leaked in GET /api/mirror-settings response";; esac

api GET /api/mirrors
[ "$(json_get "$API_BODY" '.settings.githubTokenSet')" = "true" ] || fail "GET /api/mirrors settings.githubTokenSet should be true: $API_BODY"
case "$API_BODY" in *e2e-dummy-token*) fail "token leaked in GET /api/mirrors response";; esac
echo "  PASS: token stored but never returned (githubTokenSet=true)"

api PUT /api/mirror-settings '{"rootDir":"github-mirror","intervalMinutes":720,"proxy":""}'
[ "$API_CODE" = "200" ] || fail "PUT settings without githubToken expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.intervalMinutes')" = "720" ] || fail "intervalMinutes was not updated: $API_BODY"
[ "$(json_get "$API_BODY" '.githubTokenSet')" = "true" ] || fail "omitting githubToken should keep the token: $API_BODY"
echo "  PASS: omitting githubToken keeps the stored token"

[ -f "$TEST_CFG_DIR/github-mirror.json" ] || fail "config file $TEST_CFG_DIR/github-mirror.json was not created"
CFG_OK=$(docker exec "$CONTAINER_NAME" jq -r --arg id "$MIRROR_ID" '(.settings.intervalMinutes == 720) and (.settings.githubToken == "e2e-dummy-token") and ([(.mirrors // [])[] | select(.id == $id)] | length == 0)' /config/github-mirror.json 2>/dev/null | tr -d '\r' || true)
[ "$CFG_OK" = "true" ] || fail "/config/github-mirror.json does not reflect current settings/token/mirrors"
CFG_MODE=$(docker exec "$CONTAINER_NAME" stat -c '%a' /config/github-mirror.json 2>/dev/null | tr -d '\r' || true)
[ "$CFG_MODE" = "600" ] || fail "/config/github-mirror.json should have mode 0600 (it stores the token), got '$CFG_MODE'"
echo "  PASS: settings and token persisted to /config/github-mirror.json (mode 0600)"

api PUT /api/mirror-settings '{"rootDir":"github-mirror","intervalMinutes":360,"proxy":"","githubToken":""}'
[ "$API_CODE" = "200" ] || fail "PUT settings clearing token expected 200, got $API_CODE: $API_BODY"
[ "$(json_get "$API_BODY" '.githubTokenSet')" = "false" ] || fail "githubToken \"\" should clear the token: $API_BODY"
echo "  PASS: empty githubToken clears the stored token"

echo "[e2e] All end-to-end integration tests completed successfully!"
