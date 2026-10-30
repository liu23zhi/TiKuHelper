#!/bin/sh
set -eu

source_file=${1:-/app/trusted_proxies.txt}
output_file=${2:-/tmp/trusted-proxies.generated.conf}
mkdir -p "$(dirname "$output_file")"
: > "$output_file"
[ -f "$source_file" ] || exit 0

is_ipv4() {
  old_ifs=$IFS
  IFS=.
  set -- $1
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  for octet in "$@"; do
    case "$octet" in ''|*[!0-9]*) return 1 ;; esac
    [ "$octet" -le 255 ] || return 1
  done
}

ipv4_to_int() {
  old_ifs=$IFS
  IFS=.
  set -- $1
  IFS=$old_ifs
  echo $((($1 << 24) + ($2 << 16) + ($3 << 8) + $4))
}

int_to_ipv4() {
  value=$1
  echo "$((value >> 24 & 255)).$((value >> 16 & 255)).$((value >> 8 & 255)).$((value & 255))"
}

mask_prefix() {
  old_ifs=$IFS
  IFS=.
  set -- $1
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  prefix=0
  partial=0
  for octet in "$@"; do
    case "$octet" in
      255) bits=8 ;; 254) bits=7 ;; 252) bits=6 ;; 248) bits=5 ;;
      240) bits=4 ;; 224) bits=3 ;; 192) bits=2 ;; 128) bits=1 ;; 0) bits=0 ;;
      *) return 1 ;;
    esac
    [ "$partial" -eq 0 ] || [ "$bits" -eq 0 ] || return 1
    prefix=$((prefix + bits))
    [ "$bits" -eq 8 ] || partial=1
  done
  echo "$prefix"
}

range_to_cidr() {
  start=$1
  end=$2
  is_ipv4 "$start" || return 1
  is_ipv4 "$end" || return 1
  first=$(ipv4_to_int "$start")
  last=$(ipv4_to_int "$end")
  [ "$last" -ge "$first" ] || return 1
  while [ "$first" -le "$last" ]; do
    block=1
    remaining=$((last - first + 1))
    while [ $((first % (block * 2))) -eq 0 ] && [ $((block * 2)) -le "$remaining" ]; do
      block=$((block * 2))
    done
    bits=0
    size=$block
    while [ "$size" -gt 1 ]; do size=$((size / 2)); bits=$((bits + 1)); done
    printf '%s/%s\n' "$(int_to_ipv4 "$first")" "$((32 - bits))"
    first=$((first + block))
  done
}

convert_entry() {
  token=$1
  token=${token#\[}; token=${token%\]}; token=${token#\{}; token=${token%\}}
  token=${token#\"}; token=${token%\"}; token=${token#\'}; token=${token%\'}
  token=${token#-}
  token=$(printf '%s' "$token" | sed 's/[;,]$//')
  [ -n "$token" ] || return 0
  case "$(printf '%s' "$token" | tr '[:upper:]' '[:lower:]')" in
    '*'|any|all|0/0|0.0.0.0/0|::/0) printf '%s\n' '0.0.0.0/0' '::/0'; return 0 ;;
  esac
  case "$token" in
    *-*) range_to_cidr "${token%-*}" "${token#*-}"; return $? ;;
    */*)
      address=${token%/*}; mask=${token#*/}
      if is_ipv4 "$address"; then
        case "$mask" in ''|*[!0-9]*) prefix=$(mask_prefix "$mask") || return 1 ;; *) [ "$mask" -le 32 ] || return 1; prefix=$mask ;; esac
        printf '%s/%s\n' "$address" "$prefix"
        return 0
      fi
      printf '%s' "$address" | grep -Eq '^[0-9A-Fa-f:]+$' || return 1
      printf '%s' "$mask" | grep -Eq '^[0-9]+$' || return 1
      [ "$mask" -le 128 ] || return 1
      printf '%s/%s\n' "$address" "$mask"
      return 0
      ;;
  esac
  if is_ipv4 "$token"; then printf '%s\n' "$token"; return 0; fi
  printf '%s' "$token" | grep -Eq '^[0-9A-Fa-f:]+$' || return 1
  case "$token" in *:*) printf '%s\n' "$token" ;; *) return 1 ;; esac
}

candidate_file=$(mktemp)
seen_file=$(mktemp)
trap 'rm -f "$candidate_file" "$seen_file"' EXIT HUP INT TERM

# Split the original application format without changing the source file.
awk '
{
  sub(/\r$/, ""); sub(/^\xef\xbb\xbf/, "")
  sub(/#.*/, ""); sub(/\/\/.*/, "")
  gsub(/[{}\[\]"]/, " ")
  count = split($0, segments, /[,;]/)
  for (i = 1; i <= count; i++) {
    segment = segments[i]
    sub(/^[[:space:]]*-[[:space:]]*/, "", segment)
    sub(/^[[:space:]]*(trusted_proxies|trustedProxies|trusted_proxy|trustedproxy|proxies|proxy|ips|ip)[[:space:]:=]+/, "", segment)
    sub(/^[[:space:]]*(set_real_ip_from|real_ip_from|allow|deny|from)[[:space:]]+/, "", segment)
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", segment)
    if (segment != "") { split(segment, fields, /[[:space:]]+/); print fields[1] }
  }
}
' "$source_file" > "$candidate_file"

: > "$seen_file"
candidate_count=0
valid_count=0
while IFS= read -r candidate || [ -n "$candidate" ]; do
  [ -n "$candidate" ] || continue
  candidate_count=$((candidate_count + 1))
  converted=$(convert_entry "$candidate" 2>/dev/null || true)
  if [ -z "$converted" ]; then
    printf 'trusted-proxies: ignored invalid entry: %s\n' "$candidate" >&2
    continue
  fi
  while IFS= read -r entry || [ -n "$entry" ]; do
    [ -n "$entry" ] || continue
    if ! grep -Fqx "$entry" "$seen_file"; then
      printf '%s\n' "$entry" >> "$seen_file"
      printf 'set_real_ip_from %s;\n' "$entry" >> "$output_file"
      valid_count=$((valid_count + 1))
    fi
  done <<EOF
$converted
EOF
done < "$candidate_file"

if [ "$candidate_count" -gt 0 ] && [ "$valid_count" -eq 0 ]; then
  printf 'trusted-proxies: no valid proxy entries found\n' >&2
  exit 1
fi
