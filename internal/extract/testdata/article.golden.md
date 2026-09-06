# KV Cache, Explained Again

During autoregressive decoding, a transformer recomputes attention over all previous tokens at every step unless it **caches** the key and value projections it already computed.

## Why it matters

See the [original paper](https://example.org/attention) for the full derivation. The cache trades *memory* for `O(1)` per-step compute.

> Memory is cheaper than recomputation, most of the time.

## Trade-offs

- Memory grows linearly with sequence length.
- Quantizing the cache recovers some of that budget.
  - int8 halves it again.
  - Grouped-query attention shrinks it before it is even cached.

| Precision | Bytes/token |
| --- | --- |
| fp16 | 2 |
| int8 | 1 |
