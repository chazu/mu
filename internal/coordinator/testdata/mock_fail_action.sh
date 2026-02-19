#!/bin/bash
# Mock plugin that returns an action with a failing command.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"failing","version":"0.1.0","protocol_version":1,"consumes":[],"produces":[]}'
      ;;
    plan)
      echo '{"actions":[{"id":"fail-action","command":["false"],"inputs":{},"outputs":[],"depends_on":[]}],"declared_outputs":{}}'
      ;;
  esac
done
