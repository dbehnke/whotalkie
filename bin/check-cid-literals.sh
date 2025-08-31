#!/usr/bin/env bash
# Simple lint: fail if literal CID header/attribute strings are found outside internal/cid
set -eu

# Files to ignore (internal/cid is allowed)
ignore_dir="internal/cid"

# Search for occurrences
matches=$(git grep -n --line-number --full-name -- "X-WT-CID" "wt.cid" || true)
if [ -z "$matches" ]; then
  echo "No literal occurrences found."
  exit 0
fi

# Filter out allowed file
bad=$(echo "$matches" | grep -v "^$ignore_dir/" || true)
if [ -n "$bad" ]; then
  echo "Found literal CID strings in the repository (not allowed outside $ignore_dir):"
  echo "$bad"
  exit 2
fi

echo "Only canonical definitions found in $ignore_dir."
exit 0
