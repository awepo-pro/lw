---
title: KV Cache
created: 2026-08-20
updated: 2026-08-29
type: concept
tags: [inference, memory]
sources: [raw/articles/kv-cache-explained.md]
confidence: "high"   
---



# KV Cache

The key/value cache stores per-layer attention projections from previous
decoding steps so each new token only attends to cached keys and values
instead of recomputing them.^[raw/articles/kv-cache-explained.md]

## Why it matters

Without caching, generating token n would repeat O(n) work already done for
earlier tokens. Caching turns that into a constant amount of new work per
step, at the cost of memory that grows with sequence length.

## Example

A cache-hit log line looks like the block below. The `##`-looking line inside
it is plain text, not a heading, because it sits inside a fenced code block.

```text
## this is not a heading, just log text
kv_cache_hit_rate: 0.94
```

## Related

- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost
  of reading the cache.
- [[speculative-decoding]] — both the draft and target model read the cache
  during verification.
