Binance Audit Log – Brief Documentation
Purpose

The audit log provides a persistent, searchable history of important bot decisions and runtime events for later debugging and trade analysis.

Instead of losing information from transient Docker logs, key events are written to:

/opt/coinbase/logs/audit/binance_audit.log

Rotated historical copies are compressed automatically.

Architecture
Docker logs
      ↓
docker compose logs -f bot_binance
      ↓
grep important events
      ↓
append to audit file
      ↓
logrotate (daily + compression)

Managed by:

systemd service:
binance-audit-tail.service
Service
Service name
sudo systemctl status binance-audit-tail.service
Restart service
sudo systemctl restart binance-audit-tail.service
View service logs
sudo journalctl -u binance-audit-tail.service -n 100 --no-pager
Audit File Location

Active log:

/opt/coinbase/logs/audit/binance_audit.log

Rotated archives:

binance_audit.log-YYYYMMDD.gz

Example:

binance_audit.log
binance_audit.log-20260526.gz
What Gets Logged
Trading Decisions

Captures:

Decision=BUY
Decision=SELL
Decision=FLAT

Includes:

pUp
EMA values
MACD values
thresholds
reason for block/pass

Example:

Decision=FLAT
pUp=0.55233
ema4_5m=76362.54
macdDelta_5m=-17.16
reason=bearish_macd_delta_against_buy
MA Gate

Captures 1m execution gate decisions:

[MA_GATE]

Example:

raw=BUY
final=BUY
buyMA=true
sellMA=false

Used to confirm/reject AI signals using:

LowBottom
HighPeak
MACD Gate

Captures momentum-based trade blocking:

[MACD_GATE]

Example:

raw=BUY
final=FLAT
macdHist_1m=-26.93
macdDelta_1m=-0.20
reason=bearish_macd_delta_against_buy

Purpose:

Prevent buying into bearish momentum
Prevent selling into bullish momentum
Orders / Execution

Captures:

LIVE ORDER
HOLD
FUNDS_EXHAUSTED

Useful for:

insufficient funds
blocked entries
execution issues
Risk / Position Management

Captures:

trail.activate
trail.raise
trail.trigger
runner.assign
tp.post
tp.filled
pyramid baseline met
LATCH SET BUY/SELL

Useful for:

trailing stop analysis
runner behavior
take-profit debugging
pyramiding validation
Errors / Failures

Captures:

ERROR
FATAL
panic
runtime error
SIGSEGV
stack trace

Useful for crash debugging.

Searching Logs
Recent decisions
audit-grep binance 'Decision=|MA_GATE|MACD_GATE' -n
MACD blocks
audit-grep binance 'MACD_GATE' -n -A2 -B2
MA gate blocks
audit-grep binance 'MA_GATE' -n -A2 -B2
Failed trades / funding issues
audit-grep binance 'FUNDS_EXHAUSTED|HOLD' -n
Errors / crashes
audit-grep binance 'ERROR|FATAL|panic|runtime error' -n -A5 -B5
Log Rotation

Managed automatically by:

logrotate

Config:

/etc/logrotate.d/bot-audit

Behavior:

daily rotation
keep 7 days
compress old logs
rotate early if >100MB
safe truncation while bot is writing

Current active log stays live:

binance_audit.log

Old logs:

binance_audit.log-YYYYMMDD.gz

This prevents disk growth while preserving historical debugging data.

===============================================
What we verified:

1. binance-audit-tail.service is active/running
2. selected regex logs are persisted to:
   /opt/coinbase/logs/audit/binance_audit.log
3. after rotation, the service continued writing to the active file
4. logrotate successfully compressed the old file
5. /etc/logrotate.d/bot-audit is configured for:
   daily
   rotate 7
   compress
   copytruncate
   maxsize 100M

=====================================================
AUDIT-GREP USAGE:

Usage:
  audit-grep <coinbase|binance|hitbtc> <regex> [grep-flags...]

Description:
  Search audit logs (current + rotated .gz logs) under:

    /opt/coinbase/logs/audit

  Supports:
    • Current logs
    • Rotated .gz logs
    • Regex searches
    • Standard grep flags

Examples:

  # BUY/SELL decisions + pUp
  audit-grep binance 'Decision=|Raw=|pUp=' -n --color=always

  # BUY / SELL activity
  audit-grep binance 'Raw=(BUY|SELL)|Decision=(BUY|SELL)' -n --color=always

  # MACD gate activity
  audit-grep binance 'MACD_GATE' -n -A2 -B2 --color=always

  # MA gate activity
  audit-grep binance 'MA_GATE' -n -A2 -B2 --color=always

  # Trailing stop events
  audit-grep binance 'trail\.(activate|raise|trigger)' -n -A2 -B2

  # Funding / close skips
  audit-grep binance 'FUNDS_EXHAUSTED|CLOSE-SKIP' -n --color=always

  # Live orders
  audit-grep binance 'LIVE ORDER|order\.open|postonly\.' -n --color=always

  # Errors / crashes
  audit-grep binance 'panic:|runtime error:|fatal error|SIGSEGV|ERROR|FATAL' -n -A5 -B5 --color=always

  # Around a timestamp
  audit-grep binance '2026-05-31T09:2' -n

Useful grep flags:
  -n                Show line numbers
  -A5               Show 5 lines after match
  -B5               Show 5 lines before match
  -i                Case-insensitive
  --color=always    Highlight matches

Tip:
  Use: | less -R
  to scroll while preserving colors.

=======================================================================

Another tool, why-trade, uses the same audit logs that audit-grep searches.

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
