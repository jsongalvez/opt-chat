---
name: agent-memory
description: Model-check opt-chat's subagent memory (agentmem.go) with TLA+ (formal/agentmem). Use before committing any change to agentmem.go, to the logStep/agentWriteRef path in agents.go, or to Chat.pump, Chat.build or Chat.logMsgs in chat.go, or when asked whether a subagent's step can be lost, doubled or swallowed, whether one subagent can hold another up, or whether its locks can deadlock.
---

# Subagent memory, model-checked

`formal/agentmem/AgentMem.tla` is a TLA+ model of how opt-chat feeds each
subagent's log into a chat of its own (`agentmem.go`): the subagent's own
goroutine writing a step and feeding it, Opt's `zoom("Name")`, the pump
that gives chats their compactions, a compaction saving its line,
closing idle chats past `agentOpenMax`, a write cut short, and a crash.
TLC checks it over every order of those steps within small bounds. The Go
tests pin each case TLC found; the model checks the orders no test writes.

## What it promises

| property | promise | harm if broken | Go test |
|---|---|---|---|
| NoWaitOnOthersIO | a subagent's step or Opt's zoom never waits, lock by lock, on another subagent's disk I/O | one slow fsync or one long first zoom holds up every subagent | TestAgentMemPumpNoStall, TestAgentMemCloseNoStall, TestAgentMemNoGlobalStall |
| NoDeadlock | no actors wait on each other in a cycle: the lock order on `agentMems` holds | opt-chat hangs | none: a mutant shows it can fail |
| ChatIsLog | a subagent's chat is always a first part of what its whole log parses as: no step lost, doubled or out of order, across reopens and crashes | Opt reads a step twice, or never | TestAgentMemCloses, TestAgentMemTornWrite |
| NoLostStep | every step written whole parses as a step of its own | a step after a cut-short write vanishes into the cut one's text | TestAgentMemTornTail |
| FailureSurfaces | a failure of a subagent's memory shows on the page; zoom never says a subagent with a log doesn't exist | a broken memory goes unseen, and Opt thinks the work never happened | TestAgentMemOpenFails, TestAgentMemFailureNoticed |

The off switch (`-agent-memory=false`: no chats, the old whole-log zoom)
is not modeled; TestAgentMemOff checks it.

## Does a change touch it

The model's steps and the code they stand for. Each action is one step
of the code between two lock operations.

| model | code |
|---|---|
| Lock, LockAm | `agentMems.lock` (m.mu to find the entry, then the entry's mu) |
| StartWrite, WriteStep, TornWrite | `agentWriteRef` (agents.go) → `agentMems.appendStep`: the seal (`endsBlank`, `agentMem.sealed`), `appendRaw` |
| FeedStep, LockSc, LogIO, OpenSet | `agentMems.fed` and `feed`; `Chat.logMsgs` holds the chat's mu over `appendDay` and `saveView` (fsync) |
| CloseIdle, CloseSc | `agentMems.closeIdle`: TryLock of the entry's mu, then of the chat's |
| StartZoom, View | `Agent.ZoomAgent`, `memFailed` |
| StartPump, PumpNext, PumpSc | `agentMems.pump`, kicked by `jobGate.give` and `jobGate.setMain`; `Chat.pump` |
| BuildEnd, BuildSc, BuildIO | `Chat.build` after the model call: the chat's mu over `flushNodes` and `refit` (fsync); `subChat.warn` |
| Crash | the process dies or `/reload` execs: entries, `read` and `sealed` go; the log and `.mem` stay |
| `Parse`, `Ends` | `parseSteps`/`stepHeads` (subs.go); feed's cut at the last `"\n\n"` |
| `Fixes` | which of the fixes made after b5cc5bb the model has: the checked configs have all four |

Left out: `jobGate`'s count (its mu is never held across another lock),
`setCompact` (start, quit, reload: it still holds m.mu over every chat's
mu), the main chat, a step's size and its thinking, a torn line inside
the `.mem` files (readDays skips it, and the step is fed again).

Sync commit: the model describes the code at **22af966**. To see if it
still does:

```sh
git diff 22af966 -- agentmem.go
git diff 22af966 -- agents.go chat.go subs.go | grep -nE 'logStep|appendRaw|func \(c \*Chat\) (pump|build|logMsgs|saveView|flushNodes)|stepHeads|parseSteps'
```

Empty output: the model still holds. Otherwise read the change against
the table. Move the sync commit forward only once you have confirmed the
model describes the code at the new commit.

## Run it

```sh
formal/agentmem/check.sh      # MCFixed.cfg ~4 min, MCCrashFixed.cfg seconds, on 2 cores
formal/agentmem/mutants.sh    # ~3 min; every line must say "caught"
formal/agentmem/check.sh MC.cfg MCCrash.cfg   # the code at b5cc5bb: the four bugs
```

`check.sh` downloads tla2tools 1.8.0 once (checked by sha256) into
`~/.cache/tla`, needs Java 11+, and refuses a run under 10,000 states: a
step that can never be taken passes every property. A FAIL prints the
counterexample as the actions taken; `TRACE=1` prints whole states.

## Read a result

Read every result with the `/statechart` skill: it shows each broken
property as its shortest path in plain words, and what a fix changes.
The chart inputs are here: `AgentMemLocks.tla` (two subagents, Opt
zooming one) and `AgentMemCrash.tla` (one subagent, a crash or failed
write, a failure), each with a `...Fixed.tla` that turns every fix on,
their configs, and `chart.map.mjs`, the labels. Copy `formal/agentmem` to
a scratch folder and run `/statechart from-spec` there on each, with
`--fixed`, `--map chart.map.mjs`, `--cover`. The charts and stories in
`docs/2026-10-31-subagent-memory-charts/` came from that.

A counterexample is a real bug when you can replay it through
`agentWrite`, `ZoomAgent`/`zoom` and `agentMems.pump` in a Go test; the
four found so far all did. It is drift in the model when the code checks
a condition the action doesn't: fix the model, never the property.

When a change is intended (a new lock, a new step), change the action to
match, keep each action one step between two lock operations, run
`check.sh` and `mutants.sh`, and move the sync commit.
