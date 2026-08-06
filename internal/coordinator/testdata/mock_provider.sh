#!/bin/bash
# Minimal secret provider for coordinator planning/execution tests.
while IFS= read -r line; do
  method=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["method"])' <<<"$line")
  case "$method" in
    discover)
      echo '{"name":"mock-provider","version":"0.1.0","protocol_version":1,"capabilities":["discover","resolve_secret","store_secret"]}'
      ;;
    resolve_secret)
      echo '{"value":"test-value"}'
      ;;
    store_secret)
      echo '{}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
