#!/bin/bash
# Mock PUDL-facing observer for the plugin contract tests.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock-observer","version":"0.1.0","protocol_version":1,"consumes":[],"produces":["mock_state"],"capabilities":["discover","observe"]}'
      ;;
    observe)
      echo '{"current":{"records":[{"_schema":"mock.record","value":"fixture"}]}}'
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
