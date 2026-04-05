#!/bin/bash
# Minimal NDJSON plugin for testing. Reads helper.txt and includes it in discover.
helper=$(cat "$(dirname "$0")/helper.txt" 2>/dev/null || echo "missing")
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo "{\"name\":\"test-dir\",\"version\":\"0.1.0\",\"protocol_version\":1,\"consumes\":[],\"produces\":[],\"capabilities\":[\"discover\"],\"helper\":\"$helper\"}"
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
