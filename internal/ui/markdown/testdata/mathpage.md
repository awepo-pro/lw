---
title: Quaternions
type: concept
tags: [quaternions, rotation, algebra]
confidence: high
updated: 2026-09-21
---
# Quaternions

$$q = q_0 + q_1\mathbf{i} + q_2\mathbf{j} + q_3\mathbf{k}, \qquad \mathbf{i}^2 = \mathbf{j}^2 = \mathbf{k}^2 = \mathbf{ijk} = -1$$ ^[raw/articles/quaternion.md]

A quaternion is a number with one real part and three imaginary parts whose units satisfy $\mathbf{i}^2 = \mathbf{j}^2 = \mathbf{k}^2 = \mathbf{ijk} = -1$. A point is rotated via the sandwich product $v' = qvq^{-1}$, a half-turn by $\frac{1}{2}\theta$, with axis length $\sqrt{x^2+y^2}$.

## Stray dollars in prose

Money like $5 and $6 must stay literal, and shell commands stay fenced:

```bash
NS=$(ip -o link show | awk -F': ' '/claude-vpn/ {print $2}')
ip link set $NS link up
```

A citation \[1\] and an escaped paren \(x\) are literal punctuation (A15-1).
Multi-line prose where a dollar opens one line
and closes the next: it costs $5
and $6 across lines, still literal.
