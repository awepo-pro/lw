# Panel: Scaling Inference

Moderator: Let's start with the obvious question — where does the time go?

1. Loading weights from disk.
1. The prefill pass over the prompt.
1. Decoding, one token at a time.

Panelist: Decoding dominates for long generations. That is [where the KV cache comes in](https://example.org/kv-cache).

## Q&A

> Q: Does batching help decoding?
>
> A: Yes, as long as you are not memory-bound on the cache itself.
