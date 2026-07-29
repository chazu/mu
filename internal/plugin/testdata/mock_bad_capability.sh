#!/bin/bash
while IFS= read -r line; do
  echo '{"name":"bad-capability","version":"0.1.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["observe"]}'
done
