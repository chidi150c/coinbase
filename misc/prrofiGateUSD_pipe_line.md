| Producer                                  |                                          `confMult` used for lot gate | `ProfitGateMultiplier` | Resulting `lot.ProfitGateUSD`                              |
| ----------------------------------------- | --------------------------------------------------------------------: | ---------------------: | ---------------------------------------------------------- |
| **NormalLegacy BUY/SELL**                 |                                                       `ai.Confidence` |          `1.0` default | `max(cfg.ProfitGateUSD × AIconf, .30) + recoveryAdd`       |
| **Equity BUY**                            |                                                       `ai.Confidence` |          `1.0` default | same ordinary formula                                      |
| **Equity SELL – first**                   |                                                       `ai.Confidence` |          `1.0` default | same ordinary formula                                      |
| **Equity SELL – continuation, currently** |                                                       `ai.Confidence` |        **still `1.0`** | **currently same exit profit target as first Equity SELL** |
| **Case11A SELL**                          |                   `ai.Confidence` if `>0`, otherwise **1.0 fallback** |          `1.0` default | `max(cfg.ProfitGateUSD × Case11Aconf, .30) + recoveryAdd`  |
| **Case11B BUY**                           | after our mirror: `ai.Confidence` if `>0`, otherwise **1.0 fallback** |          `1.0` default | mirrored Case11A formula                                   |
| **Case13A SELL**                          |                                                       `ai.Confidence` |               **0.50** | `max(cfg.ProfitGateUSD × AIconf, .30) × .50 + recoveryAdd` |
| **Case13B BUY**                           |                                                       `ai.Confidence` |               **0.50** | same 50% producer reduction                                |
| **Case14B BUY**                           |                                                       `ai.Confidence` |               **0.50** | same 50% producer reduction                                |
| **Case3A Replacement Mode A**             |                   does **not** use this ordinary producer calculation |                    N/A | explicitly `cfg.ProfitGateUSD`                             |
| **Case3A Replacement Mode B**             |                   does **not** use this ordinary producer calculation |                    N/A | explicitly `cfg.ProfitGateUSD`                             |



======================================


| Producer         | What continuation changes in signal decision today                                                                                                                                                         |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Case11A SELL** | First entry requires the full Pyramid SELL gate. Continuation **replaces** that with a reference-price gate. The MACD/EMA reversal conditions still remain required.                                       |
| **Case11B BUY**  | Mirrored behavior: first entry uses full Pyramid BUY gate; continuation replaces it with the reference-price gate, while MACD/EMA reversal conditions still remain required.                               |
| **Equity SELL**  | Continuation changes the Equity SELL threshold used by the producer, not the underlying producer identity. Equity still decides from its Equity trigger/funding path; AI/Logic/Pyramid remain diagnostic.  |
| **Case13A SELL** | First entry uses global SELL spacing. Continuation replaces that spacing with its own durable reference-price re-entry condition. All the other Case13A qualifications still remain required.              |
| **Case13B BUY**  | No equivalent durable continuation reference yet; its special behavior is tied to pending-count/adverse-latch logic rather than the same continuation model.                                               |
| **Case14B BUY**  | No standardized continuation mode yet. It still uses its own buffered-latch window and spacing conditions.                                                                                                 |
| **NormalLegacy** | No standardized continuation mode yet. It still uses its ordinary AI/Logic/Pyramid decision path.                                                                                                          |

========================================================

FINAL DESIGN:

ORDINARY PRODUCER
    → explicitly HIGH / MID / LOW
    → HIGH = 1.00
    → MID  = 0.75
    → LOW  = 0.50

FIRST ENTRY
    producer's existing signal qualifications
    +
    producer's native Pyramid/price admission
    → ProfitGateMultiplier = tier multiplier

CONTINUATION
    same producer's signal qualifications
    +
    same-producer/same-side last committed reference
        SELL: reference × 1.002
        BUY:  reference × 0.998
    → native Pyramid/price admission is bypassed
    → ProfitGateMultiplier = tier × 0.80

REFERENCE
    advances only from successful committed entry
    and is producer + side specific

SPECIAL
    Case3A remains exempt