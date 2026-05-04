#!/bin/bash
# Mock secret-provider plugin for testing resolve_secret and store_secret.
#
# In-memory store: a single env var STORED_VALUE captures the most recent
# store_secret value, so a test can read it back via resolve_secret on the
# ref "stored/last".
STORED_VALUE=""
STORED_REF=""
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"mock-secrets","version":"0.2.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["discover","resolve_secret","store_secret"]}'
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
        stored/last)
          if [ -n "$STORED_REF" ]; then
            echo "{\"value\":$(printf '%s' "$STORED_VALUE" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')}"
          else
            echo '{"error":"no stored value"}'
          fi
          ;;
        *)
          echo "{\"error\":\"secret not found: $ref\"}"
          ;;
      esac
      ;;
    store_secret)
      ref=$(echo "$line"   | python3 -c "import sys,json; print(json.load(sys.stdin)['secret_ref'])" 2>/dev/null)
      value=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin).get('secret_value',''))" 2>/dev/null)
      mode=$(echo "$line"  | python3 -c "import sys,json; print(json.load(sys.stdin).get('secret_mode',''))" 2>/dev/null)
      case "$mode" in
        ""|create|overwrite|create_if_absent)
          STORED_REF="$ref"
          STORED_VALUE="$value"
          echo '{}'
          ;;
        *)
          echo "{\"error\":\"unknown mode: $mode\"}"
          ;;
      esac
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
