---
title: FlashAttention
created: 2026-08-22
updated: 2026-08-27
type: concept
tags: [attention, kernels]
sources: [raw/articles/kv-cache-explained.md]
confidence: medium
---

# FlashAttention

FlashAttention is a fused attention kernel that avoids materializing the full
attention matrix in slow GPU memory, instead tiling the computation to keep
intermediate values in fast on-chip memory.^[raw/articles/kv-cache-explained.md]

## Why it matters

- Reduces memory-bandwidth traffic, which dominates attention's runtime cost.
- Produces numerically identical results to standard attention.
- Pairs naturally with KV-cache reuse during incremental decoding.

## Related

- [[kv-cache]] — the cache that flash-attention-style kernels read efficiently.
- [[gpt-4]] — a production model whose serving stack relies on fused attention
  kernels like this one.
