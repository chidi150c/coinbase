CHANGE_DESCRIPTION:

Implement a first-pass AI/model restructuring focused on improving trade-label quality, expanding feature-awareness from soft judgment gates and state context, and preserving the current execution architecture and safety model.

The objective is to improve signal quality and tradability while keeping execution safety unchanged.

1. Replace next-candle direction labels with fee-aware horizon labels

Remove the current label logic:

    if c[i+1].Close > c[i].Close {
        up = 1.0
    }

This currently trains the model to predict whether the next candle closes higher, which is noisy and poorly aligned with actual profitability.

Replace it with a fee-aware, multi-candle horizon target that predicts whether a trade would likely produce meaningful profit after fees and expected edge.

Expected behavior:

- Use a configurable prediction horizon (e.g. 10–30 candles)
- Compute future return from candle i to candle i+horizon
- Include round-trip fees and minimum trade edge
- Ignore neutral/no-edge samples

Conceptual behavior:

    futureRet := (c[i+horizon].Close - c[i].Close) / c[i].Close
    edge := (feeRatePct*2 + minEdgePct) / 100.0

    if futureRet > edge {
        label = 1.0
    } else if futureRet < -edge {
        label = 0.0
    } else {
        skip sample
    }

Goal:

Train the model to predict:

    "Is there enough future move to justify a profitable trade?"

instead of:

    "Will the next candle be green?"

2. Preserve execution architecture

Preserve the existing architecture:

    AI prediction
        ↓
    signal decision
        ↓
    gate stack
        ↓
    execute or HOLD

The AI improves signal quality only.

Existing gates must continue to decide whether a trade is executable.

No AI prediction should bypass risk, exchange, or execution constraints.

3. Convert soft judgment gates into AI features

Convert soft/contextual gates into normalized numeric model features so the model can learn when they improve or weaken trade edge.

Add the following soft judgment signals as features:

Market structure / reversal:
- HighPeak
- LowBottom

Trend context:
- EMA spread
- EMA alignment strength

Position context:
- adverse move percentage

Equity context:
- equity drawdown percentage

Price-location context:
- distance from recent high
- distance from recent low

Market regime:
- realized volatility
- volatility regime

Feature encoding:

Boolean signals:

    true  → 1.0
    false → 0.0

Continuous values:

    normalized percentages / ratios

Examples:

    HighPeak             → 0/1
    LowBottom            → 0/1
    EMA spread           → normalized %
    adverse move         → normalized %
    drawdown             → normalized %
    distance high/low    → normalized %
    volatility regime    → normalized value

The goal is to allow the model to learn contextual trade quality instead of relying solely on hard-coded heuristics.

4. Add feature-friendly state context

Add a limited set of normalized state-derived features from bot state.

Add:

Capacity context:
- SpareBuyUSD / EquityUSD
- SpareSellUSD / EquityUSD

Refund pressure:
- RefundBuyUSD / EquityUSD
- RefundSellUSD / EquityUSD

Exposure context:
- normalized BUY lot count
- normalized SELL lot count

Optional later additions (not first pass):
- timeSinceLastAddBuy
- timeSinceLastAddSell
- side-specific equity drawdown

Do NOT use model internals as features:

- weights
- bias
- FeatDim
- fit metadata

5. Keep hard gates deterministic

Do NOT convert hard execution/risk constraints into AI-only decisions.

Keep these as deterministic gates after prediction:

Funding / inventory:
- available quote/base
- spare funds
- REQUIRE_BASE_FOR_SHORT

Exchange validity:
- MIN_NOTIONAL
- ORDER_MIN_USD
- BASE_STEP
- QUOTE_STEP
- PRICE_TICK

Risk controls:
- MAX_CONCURRENT_LOTS
- pending order checks
- daily breaker
- execution validity checks

The AI may estimate opportunity quality, but hard gates must still prevent invalid, unsafe, or non-executable trades.

6. Control curse of dimensionality

Primary dimension concern is:

    rows × columns
    (training samples vs feature count)

Not model feature mismatch.

When expanding features:

- keep feature count disciplined
- prioritize strongest signals only
- avoid excessive feature growth
- maintain healthy sample-to-feature ratio

Target philosophy:

    rows >> columns

Avoid introducing too many weak state variables.

Initial target:

~12–16 strong features maximum in first pass.

This is intended to reduce overfitting risk and preserve generalization.

7. Simplify model architecture

Treat the model as a single unified logistic-learning system.

Do not distinguish conceptually between:

- basic model
- extended model

Use:

- one feature builder
- one dataset builder
- one prediction path
- one training path

All improvements should naturally flow through the same prediction/training pipeline.

8. Scope / non-goals

Do not:

- change public APIs
- change execution flow
- remove gates
- bypass risk logic
- introduce heavyweight ML or neural networks
- alter existing operational behavior

This is a targeted improvement to:

- label quality
- contextual awareness
- feature richness
- signal quality
- tradability

while preserving current safety and execution behavior.

9. File/module organization

Restructure AI-related code into clearer responsibilities:

feature_builder.go
- Build one unified feature vector used by both prediction and training.
- Include market features, soft-gate features, and selected normalized state-context features.
- Convert boolean/context gates into numeric features.
- Keep feature count disciplined to avoid curse of dimensionality.

model.go
- Keep the logistic model implementation.
- Own Predict and Fit/FitMiniBatch.
- No need to treat basic vs extended as separate conceptual paths.
- No feature-engineering logic should live here except model math.

dataset.go
- Build training datasets.
- Create fee-aware horizon labels.
- Skip neutral/no-edge samples.
- Validate dataset shape:
  - rows = number of labeled samples
  - columns = feature count
- Enforce minimum row count before training.

decide.go
- Convert model output into signal decision:
  - pUp → BUY / SELL / FLAT
- Apply signal thresholds.
- Keep decision-level context clear.
- Do not execute trades here.

step.go
- Remain responsible for orchestration and hard safety gates.
- Keep exchange/risk/funding/pending-order gates here.
- Keep order placement and close/open execution here.
- Do not move hard gates into the AI model.


Goal

We decided to simplify the AI architecture and stop maintaining:

basic model
vs
extended model

We agreed to move to:

ONE model
ONE dataset builder
ONE feature builder
ONE prediction path

You explicitly said:

state compatibility is NOT important
production tampering is acceptable
losing current state is acceptable

So we are intentionally doing a hard simplification.

What already happened
1. Backup / restore point created

Safe rollback branch:

restore-before-ai-label-rework

Working branch:

ai-label-feature-rework
2. Fee-aware labels were implemented

Old label:

if c[i+1].Close > c[i].Close {
    up = 1.0
}

Problem:

predicted next candle direction only
not profitability

New idea:

Train on:

future move over N candles
that exceeds fees + minimum edge

Current live settings:

const horizon = 15
const feeRatePct = 0.10
const minEdgePct = 0.05

Edge:

0.25%

Meaning:

BUY label:
future return > +0.25%

SELL label:
future return < -0.25%

neutral:
skip sample
3. Dataset stats after deployment

Observed:

total candles = 6000
labeled rows = 515
up = 233
down = 282
skipped = 5449
bad = 0

Interpretation:

healthy enough
balanced
not overfit-risky
4. AI behavior improved

Before:

pUp hovered around 0.49–0.51
weak/noisy

After fee-aware labels:

Observed:

0.486 → FLAT
0.534 → BUY
0.541 → BUY
0.434 → SELL

Meaning:

signal separation improved
AI became more directional
less next-candle noise
5. Gate stack mental model

We clarified:

Current architecture:

AI
↓
signal decision
↓
gate stack
↓
execute or HOLD

Example observed:

AI → SELL
↓
Decision=SELL
↓
pyramiding gate blocked
↓
HOLD

So:

AI works
gates still control execution
Curse of dimensionality discussion

Your “dimension concern” meant:

rows × columns

(statistical dimensionality)

not:

feature length mismatch

We agreed:

Current:

515 rows
8 features

Safe expansion target:

12–16 features

Avoid:

30–50 features

without enough rows.

Feature mining agreement

We agreed to convert soft judgment gates into AI features.

Soft gates to mine
HighPeak
LowBottom

Maybe later:

adverse move %
distance from recent high/low
volatility regime
spare buy/sell ratio
Hard gates stay hard

Do NOT convert:

funds checks
min notional
exchange safety
execution constraints
Major architecture decision (important)

We decided:

REMOVE split architecture

Delete conceptual distinction between:

AIMicroModel
ExtendedLogit
basic
extended
MODEL_MODE

Replace with:

ONE logistic model
Target architecture
One model

Use only:

type LogisticModel struct {
    W       []float64 `json:"W"`
    B       float64   `json:"B"`
    L2      float64   `json:"L2"`
    FeatDim int       `json:"FeatDim"`
}

No:

AIMicroModel
ExtendedLogit
One dataset builder

Replace:

buildDataset()
BuildExtendedFeatures()

with:

BuildFeaturesAndLabels(c, train)

This function must do:

feature creation
+
fee-aware horizon labels
One prediction path

Replace:

micro-model path
extended path
MODEL_MODE branching

with:

ComputePUp(c, mdl)

single path only.

decide()

Target:

func decide(
    c []Candle,
    mdl *LogisticModel,
    buyThreshold float64,
    sellThreshold float64,
    useMAFilter bool,
) Decision

No:

m *AIMicroModel
mdl *ExtendedLogit
MODEL_MODE
State

We agreed:

State compatibility does NOT matter.

Old:

Model
MdlExt

New:

Model

only.

You are okay with:

resetting state
losing current weights
fresh retraining
Current blocker before code surgery

We stopped because we needed to inspect:

Trader struct
State struct
where model fields are wired

We planned to run:

grep -R "type .*State" -n .
grep -R "AIMicroModel\|ExtendedLogit\|mdlExt\|model" -n *.go

to safely refactor:

state serialization
trader fields
load/save state
training calls
decide() wiring

without guessing.

Current objective

Next session starts with:

hard refactor to unified AI architecture
(one model / one builder / one prediction path)

Then after stabilization:

mine soft gates into features
HighPeak
LowBottom
============================================================================

The future architecture we discussed is:

new 5m candle closes
↓
append ONE mined label automatically
↓
walkforward sees new row
↓
retrain

===========================================================================================
Step 1 — Run one final raw-signal audit

Use the 6h/24h logs to test:

When Raw=BUY, was price higher 30m/60m later?
When Raw=SELL, was price lower 30m/60m later?

Proceeding with the raw directional audit on test1_raw_6h(3).log.

I’ll measure:

For every Raw=BUY:
Was price higher 30m later?
Was price higher 60m later?

For every Raw=SELL:
Was price lower 30m later?
Was price lower 60m later?

Ignoring:

FLAT
MA gate
MACD gate
actual trade execution
fees

This isolates:

"Did the AI direction itself have predictive power?"

Output will include:

BUY accuracy @30m
BUY accuracy @60m
SELL accuracy @30m
SELL accuracy @60m
Overall directional accuracy
Average move after signal
Verdict:
signal edge or no edge

______
cd ~/coinbase/monitoring

docker compose logs --since "6h" bot_binance \
| grep -E '\[TICK\] px=|\[DEBUG\] Total Lots=.*Raw=' \
> /tmp/test1_raw_6h.log

cp /tmp/test1_raw_6h.log ~/coinbase/test1_raw_6h.log

wc -l /tmp/test1_raw_6h.log
ls -lh /tmp/test1_raw_6h.log
______

==================================================

ok i want to move on to something else. I want to:
1) move buy threshold up from 0.53 to 0.574: why: because it delays AI in reaching buy exhaustion
2) I will then BUY/SELL at logic BUY/SELL when AI is FLAT or both matches as well. only block disagreement
3) Then I will expand confidence multiplier beyond thresholds starting at the models up/down avg of 0.484/0.43 (how did we even come across these figures?)
4) This will cause a lot of tiny trade amount LOTs in the state file, so exit should not be first come first save but first profitable first Exit. And the same AI-Logic is applicable to both Entry and Exit
5) monitor how pyramid adverse will be blocking 
6) trades made on AI FLAT should have target Net of their confidence multiplier value 