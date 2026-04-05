#!/bin/bash
# Mock secret-provider plugin for testing resolve_secret.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock-secrets","version":"0.1.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["discover","resolve_secret"]}'
      ;;
    resolve_secret)
      ref=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['secret_ref'])" 2>/dev/null)
      case "$ref" in
        deploy/token)
          echo '{"value":"s3cr3t-tok3n"}'
          ;;
        deploy/password)
          echo '{"value":"hunter2"}'
          ;;
        *)
          echo "{\"error\":\"secret not found: $ref\"}"
          ;;
      esac
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
