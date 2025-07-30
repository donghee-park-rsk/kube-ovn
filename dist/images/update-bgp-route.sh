#!/usr/bin/env bash
# update-bgp-route.sh
# shellcheck disable=SC2086,SC2155

set -euo pipefail

GOBGP_BIN=${GOBGP_BIN:-$(command -v gobgp || true)}
[[ -z "$GOBGP_BIN" ]] && { echo "gobgp binary not found" >&2; exit 1; }


die() { echo "ERROR: $*" >&2; exit 1; }

external_iface="net1"
external_ipv4=$(ip addr show dev "${external_iface}" | grep 'inet ' | awk '{print $2}' | cut -d'/' -f1)
# external_ipv4=$(ip route get 8.8.8.8 | grep -oP 'src \K[^ ]+')
[[ -z "$external_ipv4" ]] && die "cannot determine external IPv4 address"

exec_cmd() {
  "$@" || die "failed: $*"
}

check_inited() {
  $GOBGP_BIN global rib &>/dev/null \
    || die "gobgp global RIB not initialized (did you 'gobgp global'?)"
}

add_announced_route() {
  check_inited
  for cidr in "$@"; do
    if [[ $cidr == *:* ]]; then
      family_flag="-a ipv6"
    else
      family_flag="-a ipv4"
    fi
    exec_cmd $GOBGP_BIN global rib $family_flag add \
             "$cidr" nexthop "$external_ipv4" origin igp
  done
}

del_announced_route() {
  check_inited
  for cidr in "$@"; do
    if [[ $cidr == *:* ]]; then
      family_flag="-a ipv6"
    else
      family_flag="-a ipv4"
    fi
    exec_cmd $GOBGP_BIN global rib $family_flag del \
             "$cidr" nexthop "$external_ipv4" origin igp
  done
}


usage() {
  cat >&2 <<EOF
Usage  : $0 <add_announced_route|del_announced_route> <CIDR> [CIDR ...]
Example: $0 add_announced_route 10.100.0.0/24 192.168.1.0/24
EOF
  exit 1
}

# main entry point
main() {
  [[ $# -lt 2 ]] && usage

  local op=$1; shift
  case "$op" in
    add_announced_route) add_announced_route "$@" ;;
    del_announced_route) del_announced_route "$@" ;;
    *) usage ;;
  esac
}

main "$@"
