#!/bin/bash
# Gathers host state and emits NDJSON to stdout — one JSON line per
# schema instance. Each record includes a _schema field for routing
# and a host field for identity.
set -e

# --- OS info ---
os_id=unknown os_version=unknown os_name=unknown
if [ -f /etc/os-release ]; then
  . /etc/os-release
  os_id="${ID:-unknown}"
  os_version="${VERSION_ID:-unknown}"
  os_name="${PRETTY_NAME:-unknown}"
fi

host=$(hostname)
kernel=$(uname -r)
arch=$(uname -m)
uptime_seconds=$(awk '{print int($1)}' /proc/uptime 2>/dev/null || echo 0)

# --- linux.host ---
printf '{"_schema":"linux.host","hostname":"%s","os":{"id":"%s","version":"%s","name":"%s"},"kernel":"%s","arch":"%s","uptime_seconds":%s}\n' \
  "$host" "$os_id" "$os_version" "$os_name" "$kernel" "$arch" "$uptime_seconds"

# --- linux.package ---
if command -v dpkg-query >/dev/null 2>&1; then
  dpkg-query -W -f='${Package}\t${Version}\t${db:Status-Abbrev}\n' 2>/dev/null | \
    awk -F'\t' -v h="$host" '
      function jesc(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
      { printf "{\"_schema\":\"linux.package\",\"host\":\"%s\",\"name\":\"%s\",\"version\":\"%s\",\"status\":\"%s\"}\n", h, jesc($1), jesc($2), jesc($3) }
    '
fi

# --- linux.service ---
if command -v systemctl >/dev/null 2>&1; then
  systemctl list-units --type=service --all --no-pager --no-legend --plain 2>/dev/null | \
    awk -v h="$host" '
      function jesc(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
      { printf "{\"_schema\":\"linux.service\",\"host\":\"%s\",\"unit\":\"%s\",\"active\":\"%s\",\"sub\":\"%s\"}\n", h, jesc($1), jesc($3), jesc($4) }
    '
fi

# --- linux.filesystem ---
if command -v df >/dev/null 2>&1; then
  df -B1 --output=source,target,fstype,size,used,avail 2>/dev/null | \
    tail -n +2 | awk -v h="$host" '
      function jesc(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
      { printf "{\"_schema\":\"linux.filesystem\",\"host\":\"%s\",\"device\":\"%s\",\"mountpoint\":\"%s\",\"fstype\":\"%s\",\"size_bytes\":%s,\"used_bytes\":%s,\"avail_bytes\":%s}\n", h, jesc($1), jesc($2), jesc($3), $4, $5, $6 }
    '
fi

# --- linux.network_interface ---
# ip -j emits valid JSON; add host and _schema to each object.
if command -v ip >/dev/null 2>&1; then
  ip -j addr show 2>/dev/null | \
    python3 -c "
import sys, json
try:
    ifaces = json.load(sys.stdin)
    host = '$host'
    for iface in ifaces:
        iface['_schema'] = 'linux.network_interface'
        iface['host'] = host
        print(json.dumps(iface, separators=(',', ':')))
except:
    pass
" 2>/dev/null || true
fi

# --- linux.user ---
awk -F: -v h="$host" '
  function jesc(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
  { printf "{\"_schema\":\"linux.user\",\"host\":\"%s\",\"name\":\"%s\",\"uid\":%s,\"gid\":%s,\"home\":\"%s\",\"shell\":\"%s\"}\n", h, jesc($1), $3, $4, jesc($6), jesc($7) }
' /etc/passwd
