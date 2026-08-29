---
source_url: https://arxiv.org/abs/2211.17192
ingested: 2026-08-25
sha256: a0d00b02be77d795741c399c02d2cfe4a637da4a27acc5e6d33446b848151397
---

# Fast Inference from Transformers via Speculative Decoding

Speculative decoding proposes drafting several candidate tokens with a small,
fast model and verifying them in a single parallel forward pass of the large
target model. Accepted tokens are kept; the first rejected token is resampled
from a corrected distribution, guaranteeing the output distribution matches
standard autoregressive decoding exactly.

## Key ideas

- A draft model proposes k tokens per step.
- The target model scores all k+1 positions in one batched forward pass.
- Rejection sampling preserves the target model's output distribution.
- Speedups of 2-3x are reported with no loss in generation quality.

## Relevance

This technique depends on fast access to the target model's key/value cache
across both the draft and verification passes, which is why it pairs closely
with KV-cache engineering work.
