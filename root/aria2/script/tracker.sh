#!/usr/bin/env bash
ARIA2_CONF=${1:-/config/aria2.conf}
DOWNLOADER="curl -fsSL --connect-timeout 5 --max-time 10 --retry 2"

TRACKER=$(
  ${DOWNLOADER} https://trackerslist.com/all_aria2.txt ||
  ${DOWNLOADER} https://cdn.jsdelivr.net/gh/XIU2/TrackersListCollection@master/all_aria2.txt ||
  ${DOWNLOADER} https://trackers.p3terx.com/all_aria2.txt
)

if [ -n "${TRACKER}" ] && [ -f "${ARIA2_CONF}" ]; then
  grep -q "bt-tracker=" "${ARIA2_CONF}" || echo "bt-tracker=" >> "${ARIA2_CONF}"
  sed -i "s@^\(bt-tracker=\).*@\1${TRACKER}@" "${ARIA2_CONF}"
  echo "[tracker.sh] Trackers successfully updated in ${ARIA2_CONF}"
fi
