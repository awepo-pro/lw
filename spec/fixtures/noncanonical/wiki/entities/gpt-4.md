---
type: "entity"
title: "GPT-4"
created: 2026-08-10
updated: 2026-08-29
tags: [llm, transformers]
sources:
  - raw/papers/leviathan-2023.md
confidence: medium
---

# GPT-4

GPT-4 is a large multimodal language model whose inference cost motivates much
of the systems work on faster decoding, including speculative
decoding.^[raw/papers/leviathan-2023.md]

## Abstract

GPT-4 is a large multimodal language model whose serving cost has made
inference efficiency a first-class systems problem. Techniques such as
speculative decoding and fused attention kernels exist largely to reduce its
latency and per-token cost.

## Notes

Curator preference: use the hyphenated vendor form `gpt-4`, never `gpt4`, per
curator-memory.md.

## Related

- [[speculative-decoding]] — a technique used to reduce GPT-4's serving
  latency.
- [[flash-attention]] — a kernel-level optimization used in GPT-4-class
  serving stacks.
