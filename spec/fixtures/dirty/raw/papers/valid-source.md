---
source_url: https://example.org/papers/latency-methodology
ingested: 2026-08-12
sha256: 0040b7cd215ecd9e78f99d493d2925b7d271c18f9a59023433505436d4b804fe
---

# Reproducibility Notes for Latency Benchmarks

This note captures methodology details for measuring end-to-end request
latency and throughput under load, used as background for pages that discuss
serving-time tradeoffs.

## Method

- Fix batch size and sequence length per run.
- Warm up the server before recording measurements.
- Report p50/p95/p99 latency, not only the mean.
