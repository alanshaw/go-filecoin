#!/usr/bin/env bash

set -eo pipefail

function finish {
  local -i MAX_WAIT=60

  echo ""
  echo "cleaning up..."
  kill "$MN_PID" || true
  kill "$CL_PID" || true

  # Force KILL after MAX_WAIT seconds if the daemons don't exit
  (
    sleep $MAX_WAIT && kill -9 "$MN_PID";
    echo "Sent SIGKILL to MN, daemon failed to stop within $MAX_WAIT second at end of test";
  ) & WAITER_MN=$!

  (
    sleep $MAX_WAIT && kill -9 "$CL_PID";
    echo "Sent SIGKILL to CL, daemon failed to stop within $MAX_WAIT second at end of test";
  ) & WAITER_CL=$!

  # Wait for daemons to exit
  wait "$MN_PID"
  wait "$CL_PID"

  # Kill watchers
  kill $WAITER_MN
  kill $WAITER_CL

  rm -f "${PIECE_1_PATH}"
  rm -f "${PIECE_2_PATH}"
  rm -f "${UNSEAL_PATH}"
  rm -rf "${CL_REPO_DIR}"
  rm -rf "${MN_REPO_DIR}"
}

function free_port {
  python -c "import socket; s = socket.socket(); s.bind(('', 0)); print(s.getsockname()[1])"
}

function import_private_key {
  ./go-filecoin wallet import ./fixtures/"$1".key \
    --repodir="$2"
}

function init_local_daemon {
  ./go-filecoin init \
    --auto-seal-interval-seconds="${AUTO_SEAL_INTERVAL_SECONDS}" \
    --repodir="$1" \
    --cmdapiaddr=/ip4/127.0.0.1/tcp/"$2" \
    --genesisfile="$3"
}

function init_devnet_daemon {
    if [[ "$CLUSTER" = "test" ]]; then
        ./go-filecoin init \
            --auto-seal-interval-seconds="${AUTO_SEAL_INTERVAL_SECONDS}" \
            --repodir="$1" \
            --cmdapiaddr=/ip4/127.0.0.1/tcp/"$2" \
            --devnet-test \
            --genesisfile="http://test.kittyhawk.wtf:8020/genesis.car"
   else
        ./go-filecoin init \
            --auto-seal-interval-seconds="${AUTO_SEAL_INTERVAL_SECONDS}" \
            --repodir="$1" \
            --cmdapiaddr=/ip4/127.0.0.1/tcp/"$2" \
            --devnet-nightly \
            --genesisfile="http://nightly.kittyhawk.wtf:8020/genesis.car"
    fi
}

function start_daemon {
  ./go-filecoin daemon \
    --repodir="$1" \
    --block-time="${BLOCK_TIME}" \
    --cmdapiaddr=/ip4/127.0.0.1/tcp/"$2" \
    --swarmlisten=/ip4/127.0.0.1/tcp/"$3" &
}

function get_first_address {
  ./go-filecoin id \
    --repodir="$1" \
    | jq -r ".Addresses[0]"
}

function get_peer_id {
  ./go-filecoin id \
    --repodir="$1" \
    | jq -r ".ID"
}

function get_peers {
  ./go-filecoin swarm peers \
    --repodir="$1"
}

function wait_for_peers {
  local __peers

  __peers=$(get_peers "$1")
  until [[ ! -z "$__peers" ]]; do
    __peers=$(get_peers "$1")
    sleep 1
  done
}

function swarm_connect {
  ./go-filecoin swarm connect "$1" \
    --repodir="$2"
    local __peers

  __peers=$(get_peers "$2")
  until [[ "$__peers" = "$1" ]]; do
    __peers=$(get_peers "$2")
    sleep 1
  done
}

function chain_ls {
  ./go-filecoin chain ls --enc=json \
    --repodir="$1"
}

function wait_for_message_in_chain_by_method_and_sender {
  IFS=$'\n' # make newlines the only separator

  local __chain=""
  local __hodl=""

  while [ -z $__hodl ]; do
    __chain=$(chain_ls "$3")

    __hodl=""
    for blk in $__chain
    do
        __hodl=$(echo "$blk" | jq ".[].messages[].meteredMessage.message | select(.method == \"$1\").from | select(. == \"$2\")" 2>/dev/null | head -n 1 || true)
        if [ ! -z "$__hodl" ]; then
          break
        fi
    done

    echo "$(date "+%T") - sleeping for 10 seconds"
    echo "$__hodl"
    sleep 10
  done

  unset IFS
}

function set_wallet_default_address_in_config {
  ./go-filecoin config wallet.defaultAddress \""$1"\" \
    --repodir="$2"
}

function set_mining_address_in_config {
  ./go-filecoin config mining.minerAddress \""$1"\" \
    --repodir="$2"
}

function wait_mpool_size {
  ./go-filecoin mpool \
    --wait-for-count="$1" \
    --repodir="$2"
}

function add_ask {
  ./go-filecoin miner add-ask "$1" "$2" "$3" \
    --gas-price=0 --gas-limit=300 \
    --repodir="$4"
}

function miner_update_pid {
  ./go-filecoin miner update-peerid "$1" "$2" \
    --gas-price=0 --gas-limit=300 \
    --repodir="$3"
}

function fork_message_wait {
  eval "exec $1< <(./go-filecoin message wait $2 --repodir=$3)"
}

function join {
  cat <&"$1"
}
