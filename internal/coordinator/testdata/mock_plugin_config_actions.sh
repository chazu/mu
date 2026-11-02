#!/bin/bash
# Mock plugin that echoes target.config.actions (and optional
# target.config.declared_outputs) back as its plan. Lets tests shape exact
# action subgraphs (work_dir, output/input spellings) from the target config.
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"cfg","version":"0.1.0","protocol_version":1,"capabilities":["discover","plan"],"consumes":[],"produces":[]}'
      ;;
    plan)
      echo "$line" | python3 -c "
import sys,json
cfg=(json.load(sys.stdin).get('target') or {}).get('config') or {}
print(json.dumps({'actions': cfg.get('actions') or [], 'declared_outputs': cfg.get('declared_outputs') or {}}))
"
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
