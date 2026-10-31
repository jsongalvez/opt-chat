# Subagent memory

A subagent's steps used to live only in `agents/<Name>.log`, and
`zoom("Name")` gave that log whole. A subagent that ran for hours wrote
megabytes: a zoom of one filled or overflowed the caller's context, so a
fresh agent could not pick up its work by reading it.

Now each subagent's chat is a `Chat` of its own in `agents/<Name>.mem`,
with the main chat's code: the same 512-byte lines, pairwise merges, the
same compaction model and prompt, the same view and zoom. Its view is
16 to 32 KB (the main chat's is 64 to 128 KB), small enough for one tool
result. `zoom("Name")` gives the view, and `zoom("Name:id", n)` opens its
line `id+n` as `zoom(id, n)` opens the chat's.

It is on by default. `-agent-memory=false` turns it off: steps go to the
log only, no `.mem` chat is made, `zoom("Name")` gives the whole log as
before, and the prompt and the zoom tool say so.

```mermaid
flowchart LR
  S[subagent step] --> L[agents/Name.log]
  L --> P[zoom Name: whole log]
  P --> X[caller's context floods at 500 KB+]
  L --> F[feed: whole entries since last read]
  F --> M[agents/Name.mem: Chat]
  M --> T[tree: 512-byte lines, merges]
  T --> V[zoom Name: view, 32 KB at most]
  T --> Z[zoom Name:id, n: down to a step]
  classDef works fill:#ddd,stroke:#888,color:#000
  classDef bug fill:#f99,stroke:#c00,color:#000
  classDef fix fill:#9e9,stroke:#080,color:#000
  class S,L works
  class P,X bug
  class F,M,T,V,Z fix
```

## How it is fed

The log stays the source. Each subagent has a lock of its own, held over
a step's write to the log and the feed that follows it, and over a zoom's
feed: a feed reads the log from where it last stopped and logs the whole
entries it finds, leaving out thinking, and never reads a step half
written. (Without the lock, a zoom during a long write could cut a step
at a blank line in its text, and the rest of it was lost.) Opened again
(after a restart, or a crash between the log and the chat), a chat skips
as many steps as it holds, counted as its messages' first parts: each
step goes in once. A write cut short (a crash, a full disk) leaves no
blank line, and the next step's head, not after one, would read as text
of the cut step: so before a subagent's first step since its chat opened,
or after a failed write, a log that doesn't end in a blank line gets one.

No lock is shared by all subagents over a file read or write: a first
zoom of one long log holds up no other subagent's steps. The pump that
gives the chats a turn at the compactions copies them under the map's
lock and takes each chat's lock without it: a chat writing to disk holds
up only its own subagent.

Four locks, always taken in this order: a subagent's own, then the map of
open subagents' chats, then a chat's, then the count of compactions
running. Closing idle chats holds the map's lock, over closing their
files too, and only tries a subagent's own lock and its chat's, so it
never waits against the order, nor on a chat writing a line to disk.

A running subagent's chat is opened at its first step and built as it
works. A subagent from before this change gets its tree at its first
zoom. The zoom answers at once: a line not summarized yet shows the heads
of its first and last messages, so the first view already holds the
task, the last report and where the work went, and the rest is built in
the background. At most 32 subagents' chats stay open; past that the
least used idle ones close and open again from their files when needed.

A compaction in a subagent's chat is told whose chat it is and what its
kinds mean (`tool` there is the subagent's own call, `opt` a message Opt
sent it). A failed one shows on the page as the chat's do, named
`[Name] memory:`, and so does a subagent's chat that can't open; zoom
then says why, not that there is no such subagent.

## Cost

All subagents' chats share `-agent-jobs` compactions at once (2), and
start none while the main chat has a line waiting to be built: the main
chat's come first. A main chat line that keeps failing, waiting to be
tried again, holds up none of them. Counted on real logs, a tree takes
about 1.1 to 1.3 compactions per message, so a 500 KB log takes about 540
compactions, about $0.43 at a Haiku compaction's average of $0.0008, and
about 25 minutes at 2 at once. A 1.4 MB log takes about 1,600. A running
subagent's chat costs this as it works, whether or not it is ever
zoomed: about 1.2 compactions per step.

## Model-checked

`formal/agentmem/AgentMem.tla` models the log writer, its feed, Opt's
zoom, the pump, compactions saving their lines, closing idle chats and a
crash, each action one step of the code between two lock operations.
TLC checks five rules over every order of those steps (two subagents,
two steps each; or one subagent, three steps, a crash or failed write and
a failure): no step lost or doubled, a step or zoom never waits on
another subagent's disk, no deadlock, every failure said on the page.
`formal/agentmem/check.sh` runs it; the `agent-memory` skill says when.

At 93cb97b..b5cc5bb TLC found four bugs, each reproduced by a Go test
that failed before its fix: the pump and closing idle chats each held the
map's lock while waiting on one chat's disk writes, holding up every
other subagent's steps and zooms (TestAgentMemPumpNoStall,
TestAgentMemCloseNoStall); a write cut short swallowed the next step
(TestAgentMemTornTail); a chat that couldn't open was told only to the
terminal, and zoom said the subagent didn't exist (TestAgentMemOpenFails).
The off switch is the old whole-log zoom, checked by TestAgentMemOff, not
modeled.

The charts, drawn by /statechart from the model: grey works, red is a
bug the fix leaves, green a bug the fix fixes. Each bug's path is in
[2026-10-31-subagent-memory-charts/](2026-10-31-subagent-memory-charts/),
with its story in words.

Locks: two subagents, Opt zooming one.

```mermaid
---
title: "Subagent memory: locks"
config:
  markdownAutoWrap: false
  state:
    useMaxWidth: false
---
stateDiagram-v2
  %% second model (fix): the fix
  classDef works fill:#f4f6f7,stroke:#566573,color:#1b2631
  classDef wrong fill:#fdedec,stroke:#c0392b,color:#7b241c
  classDef fixed fill:#e9f7ef,stroke:#1e8449,color:#145a32
  classDef mixed fill:#fdebd0,stroke:#d35400,color:#7e5109
  classDef after fill:#ffffff,stroke:#808b96,color:#566573
  classDef worse fill:#f4ecf7,stroke:#7d3c98,color:#4a235a
  classDef worksPath fill:#f4f6f7,stroke:#1f3a93,stroke-width:4px,color:#1b2631
  classDef wrongPath fill:#fdedec,stroke:#1f3a93,stroke-width:4px,color:#7b241c
  classDef fixedPath fill:#e9f7ef,stroke:#1f3a93,stroke-width:4px,color:#145a32
  classDef mixedPath fill:#fdebd0,stroke:#1f3a93,stroke-width:4px,color:#7e5109
  classDef afterPath fill:#ffffff,stroke:#1f3a93,stroke-width:4px,color:#566573
  classDef worsePath fill:#f4ecf7,stroke:#1f3a93,stroke-width:4px,color:#4a235a
  state "Legend" as legend {
    state "Works today" as legendWorks
    state "Fixed or new with the fix" as legendFixed
  }
  class legendWorks works
  class legendFixed fixed
  state "A step waits on another<br/>subagent's disk" as s0
  state "A: step on disk, memory<br/>behind" as s1
  state "B: step on disk, memory<br/>behind" as s2
  state "Every step in memory" as s3
  state "Nothing written" as s4
  state "Opt's zoom waits on<br/>another subagent's disk" as s5
  [*] --> s4
  s4 --> s1 : the step is written to its log
  s1 --> s3 : feeds its memory
  s3 --> s2 : the step is written to its log
  s2 --> s3 : feeds its memory
  s3 --> s0 : takes the chat's lock to save<br/>it or the pump goes to the<br/>next chat or closes idle<br/>memories or takes its chat's<br/>lock or a subagent starts a<br/>step
  s0 --> s3 : the summary is saved to disk<br/>or its chat is written to disk
  s2 --> s0 : feeds its memory
  s3 --> s5 : the pump goes to the next chat<br/>or takes the chat's lock to<br/>save it or Opt zooms
  s5 --> s3 : the summary is saved to disk
  s3 --> s3 : its memory opens or a subagent<br/>starts a step or Opt zooms or<br/>finds its entry or takes its<br/>own lock or feeds its memory<br/>or takes its chat's lock or a<br/>compaction ends: the pump runs<br/>or the pump goes to the next<br/>chat or the pump takes a<br/>chat's lock or a summary comes<br/>back or takes the chat's lock<br/>to save it<br/>#160;<br/>with the fix, the pump goes to<br/>the next chat or closes idle<br/>memories
  s4 --> s4 : a subagent starts a step or<br/>finds its entry or takes its<br/>own lock
  class s0 fixed
  class s1 works
  class s2 works
  class s3 works
  class s4 works
  class s5 fixed
```

Crashes and failures: one subagent.

```mermaid
---
title: "Subagent memory: crashes and failures"
config:
  markdownAutoWrap: false
  state:
    useMaxWidth: false
---
stateDiagram-v2
  %% second model (fix): the fix
  classDef works fill:#f4f6f7,stroke:#566573,color:#1b2631
  classDef wrong fill:#fdedec,stroke:#c0392b,color:#7b241c
  classDef fixed fill:#e9f7ef,stroke:#1e8449,color:#145a32
  classDef mixed fill:#fdebd0,stroke:#d35400,color:#7e5109
  classDef after fill:#ffffff,stroke:#808b96,color:#566573
  classDef worse fill:#f4ecf7,stroke:#7d3c98,color:#4a235a
  classDef worksPath fill:#f4f6f7,stroke:#1f3a93,stroke-width:4px,color:#1b2631
  classDef wrongPath fill:#fdedec,stroke:#1f3a93,stroke-width:4px,color:#7b241c
  classDef fixedPath fill:#e9f7ef,stroke:#1f3a93,stroke-width:4px,color:#145a32
  classDef mixedPath fill:#fdebd0,stroke:#1f3a93,stroke-width:4px,color:#7e5109
  classDef afterPath fill:#ffffff,stroke:#1f3a93,stroke-width:4px,color:#566573
  classDef worsePath fill:#f4ecf7,stroke:#1f3a93,stroke-width:4px,color:#4a235a
  state "Legend" as legend {
    state "Works today" as legendWorks
    state "Fixed or new with the fix" as legendFixed
  }
  class legendWorks works
  class legendFixed fixed
  state "A's last write cut short" as s0
  state "A's next step swallowed by<br/>a cut one" as s1
  state "A: step on disk, memory<br/>behind" as s2
  state "Every step in memory" as s3
  state "Every step in memory, a<br/>failure said" as s4
  state "Memory failed, nobody told" as s5
  state "Nothing written" as s6
  [*] --> s6
  s6 --> s2 : the step is written to its log
  s6 --> s0 : the write is cut short or<br/>opt-chat crashes
  s2 --> s3 : its chat is written to disk or<br/>feeds its memory
  s2 --> s5 : feeds its memory
  s0 --> s5 : feeds its memory
  s0 --> s1 : the step is written to its log
  s3 --> s2 : the step is written to its log
  s3 --> s5 : feeds its memory
  s3 --> s0 : the write is cut short or<br/>opt-chat crashes
  s3 --> s4 : the summary is saved to disk
  s5 --> s1 : the step is written to its log
  s4 --> s2 : the step is written to its log
  s4 --> s0 : the write is cut short or<br/>opt-chat crashes
  s2 --> s4 : its chat is written to disk or<br/>feeds its memory
  s0 --> s2 : with the fix, the step is<br/>written to its log
  s2 --> s0 : with the fix, the write is cut<br/>short or opt-chat crashes
  s1 --> s1 : feeds its memory
  class s0 works
  class s1 fixed
  class s2 works
  class s3 works
  class s4 works
  class s5 fixed
  class s6 works
```

## Prior art

[opencode-optchat](https://github.com/ahlner/opencode-optchat) keeps a
summary tree per session, subagents' included, and keeps a child's
history private to it. UniiChat and pi-optchat page a subagent's log by
characters; neither summarizes it.
