#!/bin/bash
# Mock plugin for testing. Reads NDJSON from stdin, writes NDJSON to stdout.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock","version":"0.1.0","protocol_version":1,"consumes":["source:mock"],"produces":["mock_output"]}'
      ;;
    plan)
      echo '{"actions":[{"id":"mock-action","command":["echo","hello"],"inputs":{},"outputs":["out.txt"]}],"declared_outputs":{"mock_output":"out.txt"}}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
