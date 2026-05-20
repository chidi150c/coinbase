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
- PriceDownGoingUp
- PriceUpGoingDown

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