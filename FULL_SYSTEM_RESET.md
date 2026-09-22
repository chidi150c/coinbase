# Full System Reset Runbook

This operation cancels and reconciles open Binance orders, rebalances the live
account to approximately 50% quote-side value and 50% base-side value, clears
persisted trading state, and resumes trading from confirmed Binance balances.

> **Warning:** An authorized request changes the live BTC/USDT balance. Confirm
> that the bot and Binance bridge are running the same intended commit before
> continuing.

## 1. Verify the deployed images

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
```

Confirm that both `monitoring-bot_binance-1` and `bridge_binance` show the same
expected commit.

## 2. Resolve the bot container address

Shell variables do not survive a new login or shell. Set `BOT_IP` again whenever
starting a new terminal session:

```bash
BOT_IP=$(
  sudo docker inspect monitoring-bot_binance-1 \
    --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' |
  head -1
)

echo "BOT_IP=$BOT_IP"
```

Do not continue if the output is blank.

Example valid output:

```text
BOT_IP=172.18.0.4
```

## 3. Optional authentication test

This request must return `401 Unauthorized` and cannot start a reset:

```bash
curl -i -X POST \
  -H 'Authorization: Bearer wrong-token' \
  "http://$BOT_IP:8080/ops/full-reset"
```

## 4. Verify open-order discovery

This is read-only:

```bash
curl -sS \
  'http://127.0.0.1:8789/orders/open?product_id=BTC-USDT'
```

The response has this form:

```json
{"order_ids":[]}
```

The array may contain order IDs. The reset reconciles and cancels them using the
normal per-order broker path.

## 5. Request the live reset

```bash
FULL_RESET_TOKEN=$(
  sudo sed -n 's/^FULL_RESET_TOKEN=//p' \
    /opt/coinbase/env/bot_binance.env |
  tail -1
)

curl -i -X POST \
  -H "Authorization: Bearer $FULL_RESET_TOKEN" \
  "http://$BOT_IP:8080/ops/full-reset"

unset FULL_RESET_TOKEN
```

The immediate response should be `202 Accepted`. The reset then runs at the
next tick boundary before another `step()` begins.

## 6. Monitor completion

```bash
docker logs -f monitoring-bot_binance-1 2>&1 |
grep --line-buffered -E '\[RESET\]|step err'
```

Wait for:

```text
[RESET] completed successfully
```

If `[RESET] failed` appears, trading remains paused. Preserve the logs and fix
the reported error before retrying.

## 7. Verify completion

Confirm that no exchange orders remain:

```bash
curl -sS \
  'http://127.0.0.1:8789/orders/open?product_id=BTC-USDT'
```

Then inspect logs produced after the completion timestamp:

```bash
docker logs monitoring-bot_binance-1 \
  --since 'YYYY-MM-DDTHH:MM:SSZ' 2>&1 |
grep -E '\[RESET\]|step err|insufficient balance'
```

Replace the timestamp with the UTC completion time shown in the reset log.
