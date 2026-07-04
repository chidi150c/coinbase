Binance Bot Forensic Debugger Framework

We are building a modular forensic debugging framework for my Go-based Binance BTCUSDT trading bot. This is not a production trading feature. Its purpose is to help diagnose why the bot made or failed to make trading decisions by automatically extracting log data, generating statistics, plotting charts, and identifying root causes.

Overall Vision

Create a command-line forensic tool similar to:

bot-debug <case> [options]

Examples:

bot-debug buy-gate
bot-debug sell-gate
bot-debug threshold-stop
bot-debug runner-trail
bot-debug pyramid
bot-debug confidence
bot-debug regime
bot-debug equity-stage
bot-debug ai-threshold
bot-debug order-placement

Each case should be an independent analysis module.

Current Prototype

We have already built the first prototype:

plot-buy-gate

It:

Parses the Binance audit log.
Filters aiRaw=BUY.
Extracts:
logicEPS
logic_macd_turn
logic_macd_line
logic_macd_strong_negative
logic_macd_momentum_up
logic_pattern_buy
logicOpinion
final
Computes
gap = abs(logic_macd_turn - (-logicEPS))
Generates:
buy_gate_full.png
buy_gate_zoom.png
buy_gate_summary.txt

The prototype automatically finds the minimum gap between:

-logicEPS
logic_macd_turn

and zooms around that region.

This proved extremely useful.

Important Discovery

The forensic analysis showed:

AI BUY
Pattern BUY
Momentum UP

were all true.

However

logic_macd_strong_negative=false

prevented

logicOpinion=BUY

Therefore:

AI was NOT the blocker.
EMA Pattern was NOT the blocker.
MACD Momentum was NOT the blocker.

The blocker was MACDStrongNegative.

This type of automatic diagnosis is exactly what this framework should produce.

Future Improvements

Instead of always analyzing the entire log, support:

bot-debug buy-gate --last 1h
bot-debug buy-gate --last 6h
bot-debug buy-gate --last 24h

bot-debug buy-gate --today

bot-debug buy-gate \
    --from "2026-07-04 05:00" \
    --to   "2026-07-04 07:00"

bot-debug buy-gate --closest

bot-debug buy-gate --entry <entry_id>

bot-debug buy-gate --exit <exit_id>
Desired Output

Every forensic case should automatically produce:

1. Statistics

Example

AI BUY count

Pattern BUY count

Momentum UP count

StrongNegative count

Logic BUY count

Final BUY count

Root blocker
2. Charts

Generate charts directly from logs.

Examples:

logicEPS

logic_macd_turn

logic_macd_line

pUp

thresholds

confidence

PnL

equity stage

runner trail

etc.

Allow overlays.

3. Automatic Root Cause Detection

Instead of merely plotting data, determine:

Which gate blocked BUY?

Which gate blocked SELL?

Which threshold prevented entry?

Which stop caused exit?

Which filter never became true?

What was the closest approach?

What parameter appears too strict?

The tool should produce an evidence-based explanation rather than requiring manual inspection.

Architecture Goal

Every forensic case should be independent.

Example:

bot-debug
    buy-gate/
    sell-gate/
    threshold-stop/
    runner/
    pyramid/
    confidence/
    ai/
    regime/
    orders/

Each module should contain:

parser

metrics

charts

summary

recommendations

so new cases can be added without affecting existing ones.

Long-Term Goal

I want this to become a complete forensic toolkit for my trading bot—something that automatically explains why a decision did or did not occur. It should minimize manual log inspection by combining log parsing, derived metrics, visualizations, and rule-based diagnostics into reusable modules. The design should emphasize extensibility so future debugging cases can be added with minimal effort.


==============================================================

the code:

#!/usr/bin/env bash
set -euo pipefail

LOG_FILE="${1:-/opt/coinbase/logs/audit/binance_audit.log}"
OUT_DIR="${2:-/tmp/buy_gate_debug}"

mkdir -p "$OUT_DIR"

python3 - "$LOG_FILE" "$OUT_DIR" <<'PY'
import sys, re
from pathlib import Path
from datetime import datetime
import pandas as pd
import matplotlib.pyplot as plt

log_file = Path(sys.argv[1])
out_dir = Path(sys.argv[2])

rows = []
for line in log_file.read_text(errors="ignore").splitlines():
    if "aiRaw=BUY" not in line or "logicEPS=" not in line:
        continue

    mt = re.search(r'(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})', line)
    if not mt:
        continue

    def f(name):
        m = re.search(fr'{name}=([-+]?\d*\.?\d+)', line)
        return float(m.group(1)) if m else None

    def b(name):
        m = re.search(fr'{name}=(true|false)', line)
        return m.group(1) == "true" if m else None

    def s(name):
        m = re.search(fr'{name}=([A-Z]+)', line)
        return m.group(1) if m else None

    eps = f("logicEPS")
    turn = f("logic_macd_turn")
    if eps is None or turn is None:
        continue

    rows.append({
        "time": datetime.strptime(mt.group(1), "%Y/%m/%d %H:%M:%S"),
        "pUp": f("pUp"),
        "buyTh": f("buyTh"),
        "neg_logicEPS": -eps,
        "logicEPS": eps,
        "logic_macd_line": f("logic_macd_line"),
        "logic_macd_turn": turn,
        "gap": abs(turn - (-eps)),
        "strong_negative": b("logic_macd_strong_negative"),
        "momentum_up": b("logic_macd_momentum_up"),
        "pattern_buy": b("logic_pattern_buy"),
        "logicOpinion": s("logicOpinion"),
        "final": s("final"),
    })

df = pd.DataFrame(rows)
if df.empty:
    raise SystemExit("No aiRaw=BUY rows with logicEPS and logic_macd_turn found.")

buy_ready = (
    (df["strong_negative"] == True) &
    (df["momentum_up"] == True) &
    (df["pattern_buy"] == True)
)

closest = df.loc[df["gap"].idxmin()]
start = closest["time"] - pd.Timedelta(minutes=45)
end = closest["time"] + pd.Timedelta(minutes=45)
zoom = df[(df["time"] >= start) & (df["time"] <= end)].copy()

# Full chart
plt.figure(figsize=(12,4.5))
plt.plot(df["time"], df["neg_logicEPS"], label="-logicEPS")
plt.plot(df["time"], df["logic_macd_turn"], label="logic_macd_turn")
plt.axvspan(start, end, alpha=0.15)
plt.title("BUY gate debug: full period")
plt.xlabel("Time")
plt.ylabel("Value")
plt.grid(True)
plt.legend()
plt.gcf().autofmt_xdate()
plt.tight_layout()
plt.savefig(out_dir / "buy_gate_full.png", dpi=160)
plt.close()

# Zoom chart
plt.figure(figsize=(12,5))
plt.plot(zoom["time"], zoom["neg_logicEPS"], label="-logicEPS")
plt.plot(zoom["time"], zoom["logic_macd_turn"], label="logic_macd_turn")

ready_zoom = zoom[
    (zoom["strong_negative"] == True) &
    (zoom["momentum_up"] == True) &
    (zoom["pattern_buy"] == True)
]
if not ready_zoom.empty:
    plt.scatter(ready_zoom["time"], ready_zoom["logic_macd_turn"], label="BUY logic ready")

closest_zoom = zoom.loc[zoom["gap"].idxmin()]
plt.scatter([closest_zoom["time"]], [closest_zoom["logic_macd_turn"]], marker="x", s=90, label="closest approach")

plt.title("BUY gate debug: zoom around closest approach")
plt.xlabel("Time")
plt.ylabel("Value")
plt.grid(True)
plt.legend()
plt.gcf().autofmt_xdate()
plt.tight_layout()
plt.savefig(out_dir / "buy_gate_zoom.png", dpi=160)
plt.close()

summary = out_dir / "buy_gate_summary.txt"
summary.write_text(
f"""BUY gate debug summary

Records parsed: {len(df)}
Time range: {df['time'].min()} to {df['time'].max()}

Closest approach:
  time: {closest['time']}
  -logicEPS: {closest['neg_logicEPS']:.5f}
  logic_macd_turn: {closest['logic_macd_turn']:.5f}
  gap: {closest['gap']:.5f}

Counts:
  strong_negative=true: {(df['strong_negative'] == True).sum()}
  momentum_up=true: {(df['momentum_up'] == True).sum()}
  pattern_buy=true: {(df['pattern_buy'] == True).sum()}
  all BUY logic conditions true: {buy_ready.sum()}
  logicOpinion=BUY: {(df['logicOpinion'] == 'BUY').sum()}
  final=BUY: {(df['final'] == 'BUY').sum()}

Main interpretation:
  BUY requires strong_negative=true AND momentum_up=true AND pattern_buy=true.
  If all BUY logic conditions true is 0, the missing condition is the blocker.
""")

print(f"Created:")
print(f"  {out_dir / 'buy_gate_full.png'}")
print(f"  {out_dir / 'buy_gate_zoom.png'}")
print(f"  {summary}")
PY

=====================================================================
Then:

chmod +x ~/coinbase/monitoring/plot-buy-gate
sudo ln -sf ~/coinbase/monitoring/plot-buy-gate /usr/local/bin/plot-buy-gate

Run it anytime:

plot-buy-gate

View results:

ls -lh /tmp/buy_gate_debug
cat /tmp/buy_gate_debug/buy_gate_summary.txt

To copy chart locally from server:

scp chidi@YOUR_SERVER:/tmp/buy_gate_debug/buy_gate_zoom.png .

This gives you an automated “why no BUY?” forensic chart every time.