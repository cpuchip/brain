# Classification Benchmark: Model Comparison

*Generated from benchmark results 2026-04-02*

## The Question

Ministral 14B (local) produces classifications that "feel like slop" — entries that can't be acted on. Can API models through the Copilot SDK do better with the same prompt?

## Models Tested

| Model | Backend | Cost | Avg Latency |
|-------|---------|------|-------------|
| Ministral 14B Reasoning | LM Studio (local) | Free (GPU) | 4.1s |
| GPT-5.4 mini | Copilot SDK | 0.33x | 4.3s |
| Claude Haiku 4.5 | Copilot SDK | 0.33x | 7.9s |
| Raptor mini | Copilot SDK | 0x (FREE) | 11.5s |
| GPT-5 mini | Copilot SDK | 0x (FREE) | 21.1s |

## Results: The 8 Diagnostic Entries

These are the entries that are genuinely ambiguous — the ones where Ministral was getting it wrong. Easy entries (grocery list, birthday present, scripture study) are excluded because all models agree.

| # | Entry | Best Answer | Ministral 14B | Haiku | GPT-5.4 mini | GPT-5 mini | Raptor |
|---|-------|-------------|---------------|-------|-------------|-----------|--------|
| 4 | TITSW for parents : higbys | **people** | study ❌ | people ✅ | study ❌ | actions ⚠️ | people ✅ |
| 5 | SQUAD AI agent flow | **ideas** | study ❌ | ideas ✅ | ideas ✅ | ideas ✅ | ideas ✅ |
| 6 | AI jobs and skills | **ideas** | study ❌ | ideas ✅ | journal ⚠️ | ideas ✅ | ideas ✅ |
| 7 | Brain app scripture bug | **projects** | projects ✅ | projects ✅ | projects ✅ | actions ⚠️ | projects ✅ |
| 8 | AI wrote the article | **ideas** | people ❌ | ideas ✅ | journal ⚠️ | people ❌ | ideas ✅ |
| 9 | Claude research URL | **ideas** | projects ❌ | ideas ✅ | ideas ✅ | ideas ✅ | ideas ✅ |
| 13 | Bryce PT / HIPAA AI | **people** | projects ❌ | people ✅ | actions ⚠️ | actions ❌ | people ✅ |
| 14 | Copilot SDK URL | **ideas** | projects ❌ | ideas ✅ | ideas ✅ | ideas ✅ | ideas ✅ |
| 15 | Stripe minions URL | **ideas** | projects ❌ | ideas ✅ | ideas ✅ | ideas ✅ | ideas ✅ |

Legend: ✅ = correct, ❌ = wrong, ⚠️ = defensible but not ideal

## Accuracy Scores

| Model | Correct (strict) | Correct (with ⚠️) | Score |
|-------|------------------|-------------------|-------|
| **Claude Haiku 4.5** | 15/15 | 15/15 | **100%** |
| **Raptor mini** | 15/15 | 15/15 | **100%** |
| GPT-5 mini | 11/15 | 12/15 | 73-80% |
| GPT-5.4 mini | 11/15 | 13/15 | 73-87% |
| Ministral 14B | 7/15 | 7/15 | **47%** |

## Key Findings

### 1. Ministral 14B has two systematic failure modes

**"Study" magnet:** Anything involving learning or teaching gets pulled into "study," even when it's about tech (SQUAD, AI jobs) or people (Higbys). The word "study" in the prompt is too strong an attractor for a 14B model.

**"Projects" magnet for URLs:** Any entry with a URL gets classified as "projects," even when it's clearly just a link to explore (ideas). Ministral sees URL → "something to work on" → projects. It can't distinguish "look at this cool thing" from "build this."

### 2. Haiku and Raptor are both perfect on this test set

Both correctly identify:
- Person references (Higbys, Bryce) → people
- Tech exploration (SQUAD, AI jobs, URLs to read) → ideas
- Actual project work (bug reports, system improvements) → projects
- Real scripture study → study

### 3. GPT-5.4 mini has a "journal" tendency

On ambiguous entries with personal reflection ("where do I stand?", "I'm pretty sure AI wrote..."), GPT-5.4 mini reaches for "journal" — a category that technically exists but isn't the most actionable routing.

### 4. GPT-5 mini over-classifies as "actions"

Everything becomes a task. Bug report? Action. Teaching note about a family? Action to go teach them. This model sees the world through a to-do lens, which creates the *opposite* of the slop problem but in a different way.

### 5. Confidence calibration matters

Ministral reports 0.90 confidence on wrong answers. Haiku reports 0.82 on hard entries. Haiku's lower confidence on ambiguous entries is *more honest* — it's not sure either, but it guesses better.

## Recommendation

**Switch production classification from Ministral 14B to Raptor mini via Copilot SDK.**

Why Raptor over Haiku:
- Same quality (100% on test set)
- **Zero cost** (0x multiplier vs 0.33x)
- Acceptable latency for async background classification (11.5s vs 7.9s)

Why not GPT-5 mini (also free):
- Same 0x cost but worse quality (73-80% vs 100%)
- Much slower (21s vs 11.5s)
- Action bias makes it unreliable for routing

### Implementation

This is a **config change, not a code change.** Brain's classifier already supports both LM Studio and Copilot SDK via the `ai.Completer` interface. The switch:

1. Set `USE_COPILOT=true` (or equivalent config)
2. Set model to `raptor-mini`
3. No code changes needed

### Caveat

15 entries isn't a huge test set. The recommendation is strong enough to ship, but worth monitoring the first 50+ real classifications and adjusting if Raptor shows weaknesses on entry types not in this test set.

## Raw Results

See:
- [results-lmstudio.md](results-lmstudio.md) — Full Ministral 14B results
- [results-copilot.md](results-copilot.md) — Full Copilot SDK results (all 4 models)
