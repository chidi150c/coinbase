Help me write a CHANGE_DESCRIPTION for the code you want to modify.

The CHANGE_DESCRIPTION must be:

A single, precise sentence.

Written in imperative style (e.g., “add…”, “update…”, “replace…”, “remove…”).

Contain only the specific change required, not surrounding context or extra steps.

Avoid vague words like ‘improve’, ‘enhance’, ‘fix’ unless paired with the exact element being changed.

Use the format:
verb + what is being changed + how/with what.

For example:

add client_order_id field to the /order/market payload

update RSI_PERIOD to be configurable via environment variable RSI_PERIOD with default 14

increment bot_stoploss_triggered_total counter when a stop-loss exit occurs

Generate the CHANGE_DESCRIPTION for: {{YOUR_DESIRED_CHANGE}}.

==============================================================================================

Generate a full copy of {{FILE_NAME}} with only the necessary minimal changes to implement {{CHANGE_DESCRIPTION}}.
Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()).
Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline.
Only apply the minimal edits required to implement {{CHANGE_DESCRIPTION}}.
Return the complete file, copy-paste ready.