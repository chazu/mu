#!/bin/bash
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"erroring","version":"0.1.0","protocol_version":1,"consumes":[],"produces":[]}'
      ;;
    plan)
      echo '{"actions":[],"declared_outputs":{},"error":"compilation failed: missing input"}'
      ;;
  esac
done
