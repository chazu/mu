#!/bin/bash
# Mock plugin that emits one action which declares an output file and
# declared_outputs { state: "<output_path>" }. Used to exercise
# cross-target artifact wiring in coordinator tests.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"producer","version":"0.1.0","protocol_version":1,"capabilities":["discover","plan"],"consumes":[],"produces":["state"]}'
      ;;
    plan)
      echo '{"actions":[{"id":"emit","command":["sh","-c","echo hi > out/state.json"],"inputs":{},"outputs":["out/state.json"],"depends_on":[]}],"declared_outputs":{"state":"out/state.json"}}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
