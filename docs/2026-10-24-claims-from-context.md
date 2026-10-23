# Claims a line copies from its context

As of 2026-10-24. Follows [faithful lines](2026-10-23-faithful-lines.md).

## Which errors matter

After the fixes of 2026-10-23, a judge still found unsupported claims in
most lines. Labeled by hand on one real sample, 53 such claims were:

| label | count |
|---|---|
| a fact copied from context, wrong or stale | 3 |
| a fact copied from context, true but not in the input | 15 |
| harmless framing ("output not shown", the request a step serves) | 16 |
| the judge was wrong: the input holds it | 19 |

Only the first two mislead a later reader, and the judge's count overstates
them about threefold. A second hand check, of 15 verdicts on another sample,
found 2, 4, 0 and 9.

## Three candidates

Scored on 100 held-out messages (none used for tuning), mostly tool results,
tool calls and reports, Haiku compactions, one run each. Two judges, Sonnet
and Haiku (`eval/claims.py judge`), each list a line's facts its input
doesn't hold ("added") and the facts the baseline line took from the input
that this line drops ("lost"). A second baseline run gives the noise:
two runs of the same summarizer drop different facts.

| | added, Sonnet / Haiku | lines with any | lost vs baseline | judges agree on a line | bytes | cost per line |
|---|---|---|---|---|---|---|
| baseline | 180 / 104 | 81 / 58 | - | 65% | 403 | 1.00x |
| baseline, run again | 187 / 110 | 85 / 52 | 142 / 165 (noise) | 60% | 412 | 0.98x |
| claim check: a second call drops what the input doesn't state | 60 / 30 | 35 / 16 | 84 / 64, all real | 56% | 376 | 1.17x |
| no context for messages under 1500 bytes | 127 / 84 | 65 / 42 | 141 / 203 | 68% | 408 | 1.01x |
| ask again for a commit, long number or file name from context | 109 / 71 | 61 / 39 | 147 / 208 | 62% | 405 | 1.04x |

The claim check removes the most, but it rewrites a line it gets, so every
fact it drops is a real loss: about one per line, many of them facts the
input does state. The other two vary run to run like the baseline, so their
lost counts sit near the noise (Sonnet) or above it (Haiku). The judges
agree on whether a line adds anything for 56 to 68% of lines.

## What ships

`-compact-copied` (on): `Compact` looks in a line for a commit, a number of
five or more digits, or a source file name that its context holds and its
input doesn't, and asks once for the line again, keeping such a token only
where it names what the input refers to. It cuts the added facts by about
40% (Sonnet) and 32% (Haiku) for 4% more cost and no loss beyond the noise
on Sonnet's count. The claim check stays in `eval/claims.py check`.

Memory-eval questions (`-eval`) were not run for this: they need the whole
tree built by each summarizer, and no question set exists outside a live chat.
The lost column stands in for recall.
