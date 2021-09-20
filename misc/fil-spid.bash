#!/bin/bash

#
# Filecoin StorageProvider ID ( fil-spid.bash )
#
# A simple system for stateless miner authentication, using a custom
# `Authorization: FIL-SPID ...` HTTP header. Full spec: TODO
#
# Typical usage:
#   curl -H "Authorization: $( ./fil-spid.bash f0xxxxx )" ...
#
# The exaustive list of tasks performed by this short program is:
# - Determine daemon Host+Port+ApiToken
# - Get chain head at finality ( now - 900 epochs )
# - Determine the supplied storage-provider's Worker address at finality
# - Get the drand signature for current epoch ( now )
# - Sign the string "\x20\x20\x20{DrandSignature}" using the determined worker key
# - Print the result formatted correctly to be included in an HTTP hader
#

set -eu
set -o pipefail

die() { echo "$@" 1>&2 ; exit 1 ; }

[[ "$#" == "1" ]] || die "StorageProviderID ( f0xxxx ) as sole argument required, $# arguments provided"

FIL_SP="$1"
FIL_AUTHHDR="FIL-SPID-V0"

# we will be briefly interacting with a pretty sensitive secret: just be thorough
BIN_cat="/bin/cat"
BIN_curl="/usr/bin/curl"
BIN_jq="/usr/bin/jq"


# derive API_INFO from implied defaults if necessary
LOTUS_PATH="${LOTUS_PATH:-$HOME/.lotus}"
FULLNODE_API_INFO="${FULLNODE_API_INFO:-$( "$BIN_cat" "$LOTUS_PATH/token" ):/ip4/127.0.0.1/tcp/1234/http}"

# derive token + maddr, then host/port
IFS=':' read -r API_TOKEN API_MADDR <<<"$FULLNODE_API_INFO"
IFS='/' read -r IGNORE API_NPROTO API_HOST API_TPROTO API_PORT API_APROTO <<<"$API_MADDR"

if [[ "$API_NPROTO" == "ip6" ]]; then
  API_HOST="\[$API_HOST\]"
fi

lotus_apicall() {
  local input="$( "$BIN_cat" )"
  local output="$( "$BIN_curl" -m5 -s http://$API_HOST:$API_PORT/rpc/v0 -XPOST -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' --data "$input" )"
  local maybe_err="$( $BIN_jq -rc '.error // empty' <<<"$output" )"
  [[ -z "$maybe_err" ]] && [[ -n "$output" ]] || die -e "Error executing '$input'\n${maybe_err:-no result from API call}"
  echo "$output"
}

B64_SPACEPAD="ICAg"  # use this to pefix the random beacon, lest it becomes valid CBOR
FIL_GENESIS_UNIX="1598306400"
FIL_CURRENT_EPOCH="$(( ( $EPOCHSECONDS - $FIL_GENESIS_UNIX ) / 30  ))"
FIL_FINALIZED_TIPSET="$(
  printf '{ "jsonrpc": "2.0", "id":1, "method": "Filecoin.ChainGetTipSetByHeight", "params": [ %d, null ] }' "$(( "$FIL_CURRENT_EPOCH" - 900 ))" \
    | lotus_apicall | "$BIN_jq" -rc .result.Cids
)"
FIL_FINALIZED_WORKER_ID="$(
  printf '{ "jsonrpc": "2.0", "id":1, "method": "Filecoin.StateMinerInfo", "params": [ "%s", %s ] }' "$FIL_SP" "$FIL_FINALIZED_TIPSET" \
    | lotus_apicall | "$BIN_jq" -rc .result.Worker
)"
FIL_CURRENT_DRAND_B64="$(
  printf '{ "jsonrpc": "2.0", "id":1, "method": "Filecoin.BeaconGetEntry", "params": [ %d ] }' "$FIL_CURRENT_EPOCH" \
    | lotus_apicall | "$BIN_jq" -rc .result.Data
)"
FIL_AUTHSIG="$(
  printf '{ "jsonrpc": "2.0", "id":1, "method": "Filecoin.WalletSign", "params": [ "%s", "%s" ] }' "$FIL_FINALIZED_WORKER_ID" "${B64_SPACEPAD}${FIL_CURRENT_DRAND_B64}" \
    | lotus_apicall | "$BIN_jq" -rc '[.result.Type,.result.Data ] | join(";")'
)"

printf '%s %d;%s;%s\n' "$FIL_AUTHHDR" "$FIL_CURRENT_EPOCH" "$FIL_SP" "$FIL_AUTHSIG"
