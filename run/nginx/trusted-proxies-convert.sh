#!/bin/sh
set -eu
set -f
LC_ALL=C
export LC_ALL

source_file=${1:-/app/trusted_proxies.txt}
output_file=${2:-/tmp/trusted-proxies.generated.conf}
mkdir -p "$(dirname "$output_file")"
: > "$output_file"

is_ipv4() {
  old_ifs=$IFS
  IFS=.
  set -- $1
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  for octet in "$@"; do
    case "$octet" in ''|*[!0-9]*|0[0-9]*) return 1 ;; esac
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
if [ -f "$source_file" ]; then
awk '
{
  sub(/\r$/, ""); sub(/^\357\273\277/, "")
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
else
  : > "$candidate_file"
fi

: > "$seen_file"
candidate_count=0
valid_count=0
invalid_count=0
none_count=0
while IFS= read -r candidate || [ -n "$candidate" ]; do
  [ -n "$candidate" ] || continue
  candidate_count=$((candidate_count + 1))
  if [ "$(printf '%s' "$candidate" | tr '[:upper:]' '[:lower:]')" = none ]; then
    none_count=$((none_count + 1))
    continue
  fi
  converted=$(convert_entry "$candidate" 2>/dev/null || true)
  if [ -z "$converted" ]; then
    invalid_count=$((invalid_count + 1))
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

if [ "$none_count" -eq 1 ] && [ "$candidate_count" -eq 1 ]; then
  exit 0
fi
if [ "$invalid_count" -gt 0 ] || [ "$none_count" -gt 0 ] || { [ "$candidate_count" -gt 0 ] && [ "$valid_count" -eq 0 ]; }; then
  : > "$output_file"
  printf 'trusted-proxies: valid restricted entries or standalone none required\n' >&2
  exit 1
fi

# Match the production application boundary: explicit entries only, no implicit
# LAN ranges, wildcards, malformed mixtures or unions covering an entire family.
# Binary strings avoid lossy 128-bit arithmetic in POSIX awk.
ranges=$(awk '
function digitbits(n, width, out, i) {
  out=""; for(i=width-1;i>=0;i--) out=out int(n/(2^i))%2; return out
}
function ipv4(s, a,n,i,out) {
  n=split(s,a,"."); if(n!=4)return ""; out=""
  for(i=1;i<=4;i++){if(a[i]!~/^[0-9]+$/ || a[i]+0>255)return "";out=out digitbits(a[i]+0,8)}
  return out
}
function hexpart(s, i,n,out,c) {
  if(length(s)<1 || length(s)>4)return "";n=0
  for(i=1;i<=length(s);i++){c=index("0123456789abcdef",tolower(substr(s,i,1)))-1;if(c<0)return "";n=n*16+c}
  return digitbits(n,16)
}
function ipv6(s, halves,l,r,nh,nl,nr,i,out,pad,v) {
  nh=split(s,halves,"::");if(nh>2)return ""
  nl=halves[1]==""?0:split(halves[1],l,":")
  nr=nh==2 && halves[2]!=""?split(halves[2],r,":"):0
  if((nh==1 && nl!=8) || (nh==2 && nl+nr>=8))return ""
  out="";for(i=1;i<=nl;i++){v=hexpart(l[i]);if(v=="")return "";out=out v}
  if(nh==2)for(pad=nl+nr;pad<8;pad++)out=out "0000000000000000"
  for(i=1;i<=nr;i++){v=hexpart(r[i]);if(v=="")return "";out=out v}
  return out
}
{
  n=split($0,p,"/");if(n>2){bad=1;next}
  family=index(p[1],":")?6:4;width=family==6?128:32
  bits=family==6?ipv6(p[1]):ipv4(p[1]);prefix=n==2?p[2]+0:width
  if(bits=="" || (n==2 && p[2]!~/^[0-9]+$/) || prefix>width){bad=1;next}
  low=substr(bits,1,prefix);high=low
  for(i=prefix;i<width;i++){low=low "0";high=high "1"}
  print family,"b" low,"b" high
}
END{if(bad)exit 1}
' "$seen_file") || {
  : > "$output_file"
  printf 'trusted-proxies: invalid restricted address\n' >&2
  exit 1
}
if [ -n "$ranges" ] && ! printf '%s\n' "$ranges" | LC_ALL=C sort -k1,1 -k2,2 | awk '
function successor(s, i) {
  for(i=length(s);i>1;i--)if(substr(s,i,1)=="0")return substr(s,1,i-1) "1" substr(s,i+1)
    else s=substr(s,1,i-1) "0" substr(s,i+1)
  return s
}
{
 if($1!=family){family=$1;end=$3;fromzero=$2!~/1/}
 else if($2<=successor(end)){if($3>end)end=$3}
 else {fromzero=0;end=$3}
 if(fromzero && end!~/0/)exit 1
}
'; then
  : > "$output_file"
  printf 'trusted-proxies: unrestricted address family is not allowed\n' >&2
  exit 1
fi
