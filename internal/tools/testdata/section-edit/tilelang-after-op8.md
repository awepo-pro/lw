---
title: TileLang
created: 2026-09-23
updated: 2026-09-23
type: entity
tags: [tools, reference]
sources: [raw/papers/deepseek-v4.md]
confidence: high
---

# TileLang

A Python-embedded domain-specific language for writing high-performance GPU kernels, used by DeepSeek-AI to develop DeepSeek-V4's fused kernels — balancing development productivity with runtime efficiency, and extended by DeepSeek with host code generation and SMT-solver-assisted integer analysis. ^[raw/papers/deepseek-v4.md]

## Abstract

Without TileLang, DeepSeek-V4's elaborate architecture would have surfaced as hundreds of fine-grained Torch ATen operators; the team instead wrote a set of fused kernels in TileLang that replaced the vast majority of them, and used it to prototype attention variants quickly during architecture validation. Three contributions went back upstream: Host Codegen, which co-generates a lightweight host launcher at the IR level and cuts CPU-side per-invocation validation overhead from tens or hundreds of microseconds to under one; integration of the Z3 SMT solver into the compiler's integer analysis, unlocking more aggressive vectorization, barrier insertion and code simplification at a few seconds of compile-time cost; and a reproducibility-first numerical policy — fast-math off by default, IEEE-compliant intrinsics available, lowering rules aligned with NVCC for bitwise-identical kernel validation. A specialized TileLang kernel also computes the exact full-vocabulary KL divergence in DeepSeek-V4's on-policy distillation.

## Why a DSL

The architecture needed kernels for model development, large-scale training and production inference; TileLang's design goal is to let the same codebase support both rapid prototyping and deep iterative optimization, in close collaboration with the TileLang community. ^[raw/papers/deepseek-v4.md]

## Host Codegen

As accelerators get faster, CPU-side orchestration caps utilization. Host-side logic such as runtime contract checks is typically Python and pays a fixed per-invocation cost. TileLang co-generates the device kernel and a lightweight host launcher at the IR level, embedding metadata (data types, rank/shape constraints, stride/layout assumptions) parsed from the frontend; the launcher lowers to host source built on the TVM-FFI framework, whose compact calling convention and zero-copy tensor interop minimize overhead. Measured effect: CPU-side validation overhead drops from tens or hundreds of microseconds to less than one microsecond per invocation. ^[raw/papers/deepseek-v4.md]

## SMT-solver-assisted integer analysis

Kernel tensor-index arithmetic needs strong formal integer analysis for passes like layout inference, memory-hazard detection and bound analysis. TileLang integrates the Z3 SMT solver, translating its integer expressions into quantifier-free non-linear integer arithmetic (QF_NIA): ILP-style solving handles the standard linear cases while non-linear reasoning covers harder problems like vectorization over variable tensor shapes. Under reasonable resource limits, compilation-time overhead stays within a few seconds while improving vectorization, barrier insertion and code simplification. ^[raw/papers/deepseek-v4.md]

## Numerical precision and bitwise reproducibility

Accuracy is the default: fast-math optimizations are disabled at the compiler level, and precision-affecting approximations are explicit opt-in operators (e.g. T.__exp, T.__log, T.__sin). Where strict IEEE-754 semantics are needed, IEEE-compliant intrinsics with explicit rounding modes exist (T.ieee_fsqrt, T.ieee_fdiv, T.ieee_add). For bitwise reproducibility against hand-written CUDA baselines, algebraic simplification and lowering rules are aligned with mainstream CUDA toolchains such as NVCC, and T.annotate_layout pins layout-dependent lowering decisions so evaluation and accumulation order match the reference. These choices do not sacrifice performance: under conservative defaults TileLang kernels remain competitive, with knobs to relax numerics for speed. ^[raw/papers/deepseek-v4.md] The same bitwise-reproducibility goal drives the kernel libraries in [[batch-invariant-deterministic-kernels]], and the exact-KL kernel serves [[on-policy-distillation]]. ^[raw/papers/deepseek-v4.md]

## GPU programming model
This fine-grained GPU control is what the [[deepseek-v4]] team exploited for bit-identical validation against hand-written CUDA baselines, and the same hardware concerns — SM placement, wave quantization, distributed shared memory in thread-block clusters, split-K pitfalls — recur in [[batch-invariant-deterministic-kernels]]. ^[raw/papers/tilelang-a-composable-tiled-programming-model-for-ai-systemsthanks-mathsection-equal-contributions.md]^[raw/papers/deepseek-v4.md] Tools like [[nvitop]] make the resulting utilization and memory behavior observable on a live system. ^[raw/articles/guide-to-using-nvitop.md]
## Related

- [[deepseek-v4]] — the model whose kernels are written in TileLang
- [[batch-invariant-deterministic-kernels]] — the bitwise-reproducibility counterpart at the kernel-library level
- [[on-policy-distillation]] — consumer of the specialized exact-KL TileLang kernel
