#!/bin/bash
while IFS= read -r line; do
  echo '{"name":"bad","version":"0.1.0","protocol_version":99,"consumes":[],"produces":[]}'
done
