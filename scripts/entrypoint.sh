#!/bin/sh
set -eu

DNS_ENABLED="${SPACETRK_VM_DNS_ENABLED:-true}"
DNS_PORT="${SPACETRK_VM_DNS_PORT:-53}"
DNS_UPSTREAM="${SPACETRK_VM_DNS_UPSTREAM:-1.1.1.1,8.8.8.8}"
DNS_TAP_INTERFACE_PATTERN="${SPACETRK_VM_DNS_TAP_INTERFACE_PATTERN:-tap*}"
DNS_BRIDGE_INTERFACE="${SPACETRK_VM_DNS_BRIDGE_INTERFACE:-br-stk}"
DNS_CONF="/tmp/dnsmasq-spacetrk.conf"

start_dns() {
  # dnsmasq refuses to start if addn-hosts points at a missing file, so
  # ensure the state dir + the vm-hosts file exist before launch. The
  # orchestrator rewrites this file on VM lifecycle events and every 60s.
  mkdir -p /var/lib/spacetrk
  touch /var/lib/spacetrk/vm-hosts

  cat >"$DNS_CONF" <<EOF
port=${DNS_PORT}
domain-needed
bogus-priv
cache-size=1000
bind-dynamic
no-resolv
interface=lo
interface=${DNS_BRIDGE_INTERFACE}
interface=${DNS_TAP_INTERFACE_PATTERN}
except-interface=eth0
addn-hosts=/var/lib/spacetrk/vm-hosts
domain=vm.internal
local=/vm.internal/
expand-hosts
EOF

  OLD_IFS="$IFS"
  IFS=','
  for server in $DNS_UPSTREAM; do
    trimmed="$(echo "$server" | tr -d '[:space:]')"
    if [ -n "$trimmed" ]; then
      echo "server=${trimmed}" >>"$DNS_CONF"
    fi
  done
  IFS="$OLD_IFS"

  if dnsmasq --conf-file="$DNS_CONF"; then
    echo "spacetrk: dnsmasq started on port ${DNS_PORT}"
  else
    echo "spacetrk: failed to start dnsmasq" >&2
    return 1
  fi
}

if [ "$DNS_ENABLED" = "true" ]; then
  start_dns
else
  echo "spacetrk: gateway DNS startup disabled"
fi

exec /app/server
