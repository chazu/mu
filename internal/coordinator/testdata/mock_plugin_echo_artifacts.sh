#!/bin/bash
# Mock plugin that echoes toolchain_artifacts back in a verifiable way.
# Encodes the artifacts JSON as a base64 string in the action command to avoid escaping issues.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock","version":"0.1.0","protocol_version":1,"consumes":[],"produces":["mock_output"]}'
      ;;
    plan)
      # Check if toolchain_artifacts is non-null and non-empty.
      has_artifacts=$(echo "$line" | python3 -c "
import sys,json
d=json.load(sys.stdin)
a=d.get('toolchain_artifacts') or {}
print('yes' if len(a)>0 else 'no')
" 2>/dev/null)
      echo "{\"actions\":[{\"id\":\"mock-action\",\"command\":[\"echo\",\"has_artifacts=${has_artifacts}\"],\"inputs\":{},\"outputs\":[],\"depends_on\":[]}],\"declared_outputs\":{}}"
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
