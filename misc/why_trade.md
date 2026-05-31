why-trade is a diagnostic helper that explains why the bot did or did not trade around a price, timestamp, or order ID. It searches Binance audit logs, finds the nearest TRACE step.start, then prints the decision/gate/order chunk around that moment.

Usage:
  why-trade price <PRICE> [-s BUY|SELL] [-l LOGFILE] [-w LINES] [--chunk | --chunk-filter]
  why-trade ts    <TIMESTAMP_SUBSTR> [-s BUY|SELL] [-l LOGFILE] [-w LINES] [--chunk | --chunk-filter]
  why-trade oid   <ORDER_ID> [-s BUY|SELL] [-l LOGFILE] [--chunk | --chunk-filter]

Description:
  why-trade explains what the bot was doing near a price, timestamp, or order ID.

  It searches the Binance audit log, finds the closest trading step, then shows
  the surrounding decision, gate, order, funding, pyramid, and post-only events.

  Default log:
    ${LOG_DEFAULT}

Modes:
  price <PRICE>
    Finds the TRACE step.start line whose price is closest to PRICE.

  ts <TIMESTAMP_SUBSTR>
    Finds the first log line matching the timestamp substring, then shows the
    nearby trading step.

  oid <ORDER_ID>
    Searches current and rotated audit logs for an order ID, then walks backward
    to the nearest step.start.

Flags:
  -s BUY|SELL
    Side-aware filtering for gate/order patterns.
    Default: ${SIDE_DEFAULT}

  -l LOGFILE
    Use a different audit log.
    Default: ${LOG_DEFAULT}

  -w LINES
    Half-window context around the matched step.
    Default: ${WINDOW_DEFAULT}

  --chunk
    Show the full raw step chunk from nearest step.start to the next step.start.

  --chunk-filter
    Show only key lines from the step chunk:
      decisions
      gates
      order opens/closes
      post-only events
      funding errors
      pyramid blocks
      flat/hold output

Examples:
  why-trade price 74068.07 --chunk-filter

  why-trade price 115,303.99 -s SELL --chunk-filter

  why-trade ts 2026-05-31T09:25 --chunk-filter

  why-trade oid 50919131900 -s BUY --chunk-filter

  why-trade price 74068.07 -w 150

Typical workflow:
  1. Use audit-grep to find a suspicious BUY/SELL/order:
       audit-grep binance 'Decision=SELL|Decision=BUY|LIVE ORDER|postonly\.filled' -n

  2. Copy the price, timestamp, or order ID.

  3. Use why-trade to explain that moment:
       why-trade price <PRICE> --chunk-filter
       why-trade ts <TIME> --chunk-filter
       why-trade oid <ORDER_ID> --chunk-filter

Notes:
  Use --chunk-filter first for clean output.
  Use --chunk when you need the full raw step.
  Use -s SELL when investigating sell-side gates.
  Use -s BUY when investigating buy-side gates.
  
=======================================================================

why-trade uses the same audit logs that audit-grep searches.

But:

audit-grep does not produce the logs.

The logs are produced by:

binance-audit-tail.service

Pipeline:

bot_binance docker logs
→ binance-audit-tail.service
→ /opt/coinbase/logs/audit/binance_audit.log

Then both tools read that same file:

audit-grep  → searches the audit log
why-trade   → analyzes the audit log around a time/price/order
