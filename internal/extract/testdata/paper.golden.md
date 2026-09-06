We propose speculative decoding, which uses a small draft model to propose several tokens that a larger target model verifies in a single forward pass.

This yields a **2-3x** wall-clock speedup with *no change* to the target model's output distribution.

```
def verify(draft_tokens, target_logits):
    accepted = 0
    for t in draft_tokens:
        if accept(t, target_logits):
            accepted += 1
        else:
            break
    return accepted
```
