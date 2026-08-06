#!/bin/bash
# Echo target-level sealed output declarations onto the planned action.
while IFS= read -r line; do
  method=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["method"])' <<<"$line")
  case "$method" in
    discover)
      echo '{"name":"sealed-output","version":"0.1.0","protocol_version":1,"capabilities":["discover","plan"]}'
      ;;
    plan)
      python3 -c '
import json, sys
request = json.load(sys.stdin)
target = request["target"]
print(json.dumps({
    "actions": [{
        "id": "write",
        "command": ["write-secret"],
        "inputs": {},
        "outputs": [],
        "sealed_outputs": target.get("sealed_outputs", {}),
        "sealed_output_modes": target.get("sealed_output_modes", {}),
    }],
    "declared_outputs": {},
}))
' <<<"$line"
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
