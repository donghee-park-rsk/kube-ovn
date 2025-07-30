#!/usr/bin/env bash
# update-bgp-route.sh
# shellcheck disable=SC2086,SC2155

set -euo pipefail

GOBGP_BIN=${GOBGP_BIN:-$(command -v gobgp || true)}
[[ -z "$GOBGP_BIN" ]] && { echo "gobgp binary not found" >&2; exit 1; }

die() { echo "ERROR: $*" >&2; exit 1; }

external_iface="net1"
external_ipv4=$(ip addr show dev "${external_iface}" | grep 'inet ' | awk '{print $2}' | cut -d'/' -f1)
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

parse_key_value_args() {
  local add_routes=""
  local del_routes=""
  
  # find key=value
  for arg in "$@"; do
    case "$arg" in
      add_announced_route=*)
        add_routes="${arg#*=}"  # extract after = values
        ;;
      del_announced_route=*)
        del_routes="${arg#*=}"
        ;;
      *)
        echo "Unknown argument: $arg" >&2
        usage
        ;;
    esac
  done

  if [[ -n "$del_routes" ]]; then
    echo "Processing del_announced_route: $del_routes"
    # change cidrs to array
    IFS=',' read -ra del_cidrs <<< "$del_routes"
    # remove leading and trailing spaces
    for i in "${!del_cidrs[@]}"; do
      del_cidrs[$i]=$(echo "${del_cidrs[$i]}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    done
    del_announced_route "${del_cidrs[@]}"
  fi
  
  if [[ -n "$add_routes" ]]; then
    echo "Processing add_announced_route: $add_routes"
    # change cidrs to array
    IFS=',' read -ra add_cidrs <<< "$add_routes"
    # remove leading and trailing spaces from each CIDR
    for i in "${!add_cidrs[@]}"; do
      add_cidrs[$i]=$(echo "${add_cidrs[$i]}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    done
    add_announced_route "${add_cidrs[@]}"
  fi
}

usage() {
  cat >&2 <<EOF
Usage Options:
  1. Traditional: $0 <add_announced_route|del_announced_route> <CIDR> [CIDR ...]
  2. Key-Value Arguments: $0 add_announced_route=CIDR1,CIDR2 [del_announced_route=CIDR3,CIDR4]

Examples:
  $0 add_announced_route 10.100.0.0/24 192.168.1.0/24
  $0 add_announced_route=10.100.0.0/24,10.100.1.0/24 del_announced_route=10.0.0.0/24,10.0.1.0/24
  $0 add_announced_route=10.100.0.0/24,10.100.1.0/24
  $0 del_announced_route=10.0.0.0/24,10.0.1.0/24
EOF
  exit 1
}


has_key_value_args() {
  for arg in "$@"; do
    case "$arg" in
      *=*)
        return 0  # key=value
        ;;
    esac
  done
  return 1  # key=value not found
}

# main entry point
main() {
  # if no arguments are provided, show usage
  [[ $# -eq 0 ]] && usage
  
  # check if key=value arguments are used
  if has_key_value_args "$@"; then
    parse_key_value_args "$@"
    return 0
  fi
  
  [[ $# -lt 2 ]] && usage
  
  local op=$1; shift
  case "$op" in
    add_announced_route) add_announced_route "$@" ;;
    del_announced_route) del_announced_route "$@" ;;
    *) usage ;;
  esac
}

main "$@"
