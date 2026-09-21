---
title: Speculative Decoding
created: 2026-08-25
updated: 2026-08-29
type: concept
tags: [inference, decoding]
sources: [raw/papers/leviathan-2023.md]
confidence: high
---

# Speculative Decoding

Speculative decoding accelerates autoregressive generation by drafting several
candidate tokens with a small model and verifying them in one parallel pass of
the large target model.^[raw/papers/leviathan-2023.md]

## Abstract

Speculative decoding drafts a short continuation with a small model and has the
large target model verify the whole continuation in one forward pass, accepting
the longest prefix it agrees with. This accelerates autoregressive generation
without changing the target model's output distribution.

## How it works

A cheap draft model proposes a short continuation. The target model then
scores the whole continuation in a single forward pass and accepts the
longest prefix that matches what it would have sampled itself, resampling the
first rejected token from a corrected distribution.

## Related

- [[kv-cache]] — both the draft and target model need fast cache access during
  verification.
- [[GPT-4]] — a production model where inference latency motivates techniques
  like this one.
