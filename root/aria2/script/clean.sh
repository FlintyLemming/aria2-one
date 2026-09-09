#!/usr/bin/env bash
# $1: GID, $2: File count, $3: File path
FILE_PATH="$3"
if [ -n "$FILE_PATH" ] && [ -f "${FILE_PATH}.aria2" ]; then
  rm -f "${FILE_PATH}.aria2"
fi
