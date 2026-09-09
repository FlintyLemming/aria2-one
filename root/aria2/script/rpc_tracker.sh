#!/usr/bin/env bash
DOWNLOADER="curl -fsSL --connect-timeout 5 --max-time 10 --retry 2"
TRACKER=$(
  ${DOWNLOADER} https://trackerslist.com/all_aria2.txt ||
  ${DOWNLOADER} https://cdn.jsdelivr.net/gh/XIU2/TrackersListCollection@master/all_aria2.txt
)

if [ -n "${TRACKER}" ]; then
  PAYLOAD="{\"jsonrpc\":\"2.0\",\"method\":\"aria2.changeGlobalOption\",\"id\":\"tracker_cron\",\"params\":[{\"bt-tracker\":\"${TRACKER}\"}]}"
  curl -fsSd "${PAYLOAD}" "http://127.0.0.1:6800/jsonrpc" > /dev/null 2>&1
  echo "[rpc_tracker.sh] Trackers updated via RPC hot-reload."
fi
