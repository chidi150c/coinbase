Forensic Producer Synthesis (FPS)
Definition

Forensic Producer Synthesis (FPS) is a disciplined engineering methodology for creating new independent BUY or SELL producers from historical why-trade observations.

Starting from a historical decision point, FPS analyzes every available raw material, identifies the market phenomenon present at that instant, and synthesizes the minimum combination of raw materials required to represent that phenomenon as a new independent producer.

The historical outcome is used to identify an event worthy of investigation. The synthesized producer is then validated against additional historical occurrences before becoming part of the trading engine.

Objective

Transform

Historical why-trade event

into

Independent BUY/SELL producer

that represents a specific market phenomenon.

Methodology
Step 1 — Select the forensic event

Choose a historical why-trade timestamp where a BUY or SELL opportunity is believed to exist.

Example

why-trade ts 2026-08-01T18:31
Step 2 — Freeze the decision state

Treat the timestamp as an immutable snapshot.

Collect every raw material that existed at that decision instant.

Step 3 — Inventory all raw materials

Do not begin designing the producer.

First enumerate every available input.

Examples include:

AI

Raw
Confidence
Probability
Thresholds

MACD

Line
LinePrev6
Turn
Histogram
DHist
DSmooth
Momentum

EMA

Spread
EMA2050
LowBottom
HighPeak
PatternBuy
PatternSell

Price Structure

RecentLow
RecentHigh
NearRecentLow
NearRecentHigh

Pyramid

Spacing
Gate
Adverse
Latch
Decay

Market

Regime
RegimeMultiplier
FreshLow
FreshHigh

Inventory

Lots
Dust
Pending

Equity

Baseline
Triggers
Distances
Sizing
Step 4 — Identify the market phenomenon

Ask

What market phenomenon do these raw materials describe?

Examples

Capitulation Bottom

Peak Reversal

Momentum Exhaustion

Failed Breakdown

Breakout Continuation

The producer models the phenomenon—not the future outcome.

Step 5 — Select the minimum raw-material combination

Choose only the raw materials necessary to describe the phenomenon.

Each selected raw material should contribute a unique purpose.

Example

Purpose	Raw Material
Direction	AI Raw
Quality	AI Confidence
Market Context	Regime
Location	Price Near Recent Low
Trend Persistence	MACD LinePrev6
Current Trend	MACD Line
Momentum	MACD Histogram
Structure	EMA LowBottom
Step 6 — Design the independent producer

Implement the phenomenon as a standalone producer.

Example

capitulationBottomBuy := ...

The producer must:

be self-contained
have a clear objective
produce BUY or SELL independently of other producers
Step 7 — Factor the producer (optional)

If the phenomenon naturally separates into:

persistent setup
final confirmation

split it into:

ProducerArm

and

Producer

Example

capitulationBuyArm := ...

capitulationBottomBuy :=
    capitulationBuyArm &&
    ema.LowBottom

The Arm is optional.

Not every producer requires one.

Step 8 — Define the producer

Document

Objective
Market phenomenon
Raw materials used
Raw materials intentionally excluded
Decision source
Diagnostics
Invalidations
Step 9 — Historical validation

Search historical why-trade data.

Evaluate

true positives
false positives
missed opportunities
losing trades

Refine only if necessary.

Deliverables

Every FPS exercise should produce:

1.
Historical timestamp

2.
Market phenomenon

3.
Complete raw-material inventory

4.
Selected raw materials

5.
Rejected raw materials

6.
Independent producer

7.
(Optional) Producer Arm

8.
Decision source

9.
Diagnostics

10.
Invalidation rules

11.
Historical validation results
Design Principles
Build producers from market phenomena, not implementation convenience.
Select the minimum sufficient set of raw materials.
Every producer must have a single, clearly defined objective.
Prefer independent producers over modifying existing ones.
Use an Arm only when the phenomenon naturally separates into a persistent setup and a final confirmation.
Validate every synthesized producer against multiple historical occurrences before promoting it to production.
Expected Outcome

Repeated application of Forensic Producer Synthesis builds a library of specialized independent producers, each representing a distinct market phenomenon.

Example evolution:

Case 11A
Peak Reversal SELL Producer

↓

Case 11B
Bottom Reversal BUY Producer

↓

Case 13
Capitulation Bottom BUY Producer

↓

Future Cases
Momentum Exhaustion BUY
Failed Breakdown BUY
Breakout Continuation BUY
Liquidity Sweep SELL
...

Over time, the trading engine evolves from a collection of generic rules into a library of well-defined, independently validated market-phenomenon producers.