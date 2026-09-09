#!/usr/bin/env bash
set -e

echo "[test] checking bash scripts syntax..."
for f in root/aria2/script/*.sh root/etc/cont-init.d/* root/etc/services.d/*/run; do
  if [ -f "$f" ]; then
    bash -n "$f"
    echo "  PASS: $f"
  fi
done

echo "[test] testing clean.sh behavior..."
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

TEST_FILE="$TMP_DIR/downloaded.iso"
ARIA_FILE="$TEST_FILE.aria2"
touch "$TEST_FILE" "$ARIA_FILE"

bash root/aria2/script/clean.sh "dummy_gid" "1" "$TEST_FILE"
if [ -f "$ARIA_FILE" ]; then
  echo "FAIL: $ARIA_FILE was not removed by clean.sh"
  exit 1
fi
if [ ! -f "$TEST_FILE" ]; then
  echo "FAIL: $TEST_FILE should NOT be removed by clean.sh (only .aria2)"
  exit 1
fi
echo "  PASS: clean.sh removes only .aria2 file"

echo "All script tests passed successfully."
