# llmwiki v1.1 — beyond the next version

**Status:** parked. Items here need v1.0 shipped first, or need evidence we don't have.
**Reads with:** [`PLAN.md`](PLAN.md) (v0.1) · [`PLAN-v1.md`](PLAN-v1.md) (v1.0)

---

## 1. Image-based PDFs and OCR

v1.0 handles **text-based** PDFs via Docling. Scanned pages, photographed documents and
figure-heavy papers need a different pipeline, and that pipeline is where document
extraction stops being a solved problem.

**Deferred because** OCR quality varies enormously by document, and bad extraction is
worse than refused extraction — a wiki compiled from garbled OCR is confidently wrong,
which is the one failure mode this whole project exists to prevent.

**Scope**

- **OCR backend** behind the same `internal/extract` interface v1.0 establishes. Likely
  MinerU (PaddleOCR-based, strong on CJK and formula-dense layouts, converts tables to
  HTML and formulas to LaTeX) or a Docling OCR pipeline.
- **GPU-optional.** CPU OCR is slow but must work; GPU is a speedup, never a requirement.
- **Confidence gating, hard.** Per-page OCR confidence surfaced in the changeset. Pages
  below threshold are staged as `raw/` material *flagged unreliable*, and the agent is
  told not to synthesize confident claims from them.
- **Human spot-check in review.** For OCR'd sources, the review screen shows extracted
  text beside the page image so you can sanity-check before the agent's pages land.
- **Figures and tables** — extract as assets into `raw/assets/`, reference from the
  source page rather than trying to linearise them into prose.

**Open question for that time:** whether a VLM pass (send the page image to a
vision-capable model) beats classical OCR for our documents. It likely does for figures
and tables, at meaningfully higher cost. Measure before committing.

---

## 2. Ideas needing evidence first

These are recorded so they aren't lost, not because they're committed.

| Idea | What would justify building it |
|---|---|
| **Audio/video ingest** | Transcripts are a first-class `raw/` type already. Worth it if you actually accumulate talks and podcasts — Docling handles WAV/MP3, so the marginal cost is low |
| **Collaborative vaults** | Multi-writer review with per-reviewer approval. Only meaningful if more than one person reviews the same vault; the journal already records `by whom`, so the data model is ready |
| **Vault diffing / merge** | Compare two vaults on the same domain, or merge a colleague's. Hard, and pointless before multi-vault (v1.0 item 5) lands |
| **Agent-proposed schema changes** | Let the agent propose taxonomy edits to `SCHEMA.md` — new tags, retired ones — through the same review gate. Attractive, but schema churn invalidates existing pages, so it needs a migration story first |
| **Confidence decay** | Auto-downgrade `confidence` on pages whose sources have aged past a domain-specific horizon. Needs real usage data to pick horizons that aren't arbitrary |
| **Mobile/web review** | Approve changesets from a phone. Only worth it once nightly automated ingest (v0.1's CI-able pipeline) is genuinely part of your routine |
| **Vault templates** | Ship starter `SCHEMA.md` files for common domains (ML research, legal, medicine, a codebase). Cheap, but needs a few real vaults first to know what a good schema looks like |
