---
source_url: https://example.org/articles/kv-cache-explained
ingested: 2026-08-20
sha256: 5dc385ee8ee4b2719ad6d1521626fb315812a812e9bb925fef4861541800dd7b
---

# KV Cache, Explained

During autoregressive decoding, a transformer recomputes attention over all
previous tokens at every step unless it caches the key and value projections
from earlier steps. The KV cache stores these projections so each new token
only requires computing attention against the cached keys and values, not
recomputing them from scratch.

## Why it matters

- Turns per-token cost from O(n) attention work back to O(1) incremental work.
- Memory grows linearly with sequence length and batch size.
- Techniques like multi-query and grouped-query attention shrink the cache.
- Flash-attention style kernels reduce the memory-bandwidth cost of reading
  the cache back during each decoding step.
