#!/bin/bash
# Mock secret provider for coordinator tests using scheme "env".
# Resolves any ref to a fixed dummy value so Plan's secret-resolution step
# succeeds; we only assert that sealed_inputs were ATTACHED to the action.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"env","version":"0.1.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["discover","resolve_secret"]}'
      ;;
    resolve_secret)
      echo '{"value":"dummy-resolved-value"}'
      ;;
    plan)
      echo '{"actions":[],"declared_outputs":{}}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
