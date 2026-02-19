#!/bin/bash
# Mock plugin for coordinator Build() integration tests.
# Responds to discover and plan over NDJSON.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock","version":"0.1.0","protocol_version":1,"consumes":[],"produces":["mock_output"]}'
      ;;
    plan)
      echo '{"actions":[{"id":"mock-action","command":["echo","hello"],"inputs":{},"outputs":[],"depends_on":[]}],"declared_outputs":{}}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
