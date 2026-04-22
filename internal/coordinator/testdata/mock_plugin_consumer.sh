#!/bin/bash
# Mock plugin that reads deps[0].artifacts.state and declares an action
# using that path as an input. This exercises the cross-target artifact
# pipeline: if the coordinator correctly plumbed the producer's
# declared_outputs into DepInfo.Artifacts, "state_path" will be a real
# project-relative path that Resolve must treat as a deferred cross-target
# input (not hash from disk).
while IFS= read -r line; do
  method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['method'])" 2>/dev/null)
  case "$method" in
    discover)
      echo '{"name":"consumer","version":"0.1.0","protocol_version":1,"capabilities":["discover","plan"],"consumes":["state"],"produces":[]}'
      ;;
    plan)
      state_path=$(echo "$line" | python3 -c "
import sys,json
d=json.load(sys.stdin)
deps=d.get('deps') or []
for dep in deps:
    arts=dep.get('artifacts') or {}
    if 'state' in arts:
        print(arts['state'])
        sys.exit(0)
print('')
" 2>/dev/null)
      if [ -z "$state_path" ]; then
        echo '{"error":"consumer plugin saw no state artifact in deps"}'
      else
        python3 -c "
import json
spec = {
  'actions': [{
    'id': 'ingest',
    'command': ['sh', '-c', 'cat $state_path'],
    'inputs': {'state': '$state_path'},
    'outputs': [],
    'depends_on': []
  }],
  'declared_outputs': {}
}
print(json.dumps(spec))
"
      fi
      ;;
    *)
      echo '{"error":"unknown method"}'
      ;;
  esac
done
