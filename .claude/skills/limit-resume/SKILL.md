---
name: limit-resume
description: Model-check opt-chat's resume at a usage limit's reset with TLA+ (formal/limitresume). Use before committing any change to limitresume.go, the limit parts of limits.go, or the limitHit/limitWent/noteLimits callers in agents.go, main.go and claude.go, or when asked whether the limit resume can wait on the wrong failure, resume early or late, resume twice, or lose a resume.
---

# The limit resume, model-checked

`formal/limitresume/LimitResume.tla` is a TLA+ model of how opt-chat
resumes Opt's turn and the subagents a usage limit stopped. TLC checks it
over every order of steps within small bounds. The Go tests check each
step's inside (the parser, the wording, the fake Claude); the model checks
the orders the tests never write.

## What it promises

Each property, the harm if it breaks, and the Go test that pins the case
TLC first found:

| property | promise | harm if broken | Go test |
|---|---|---|---|
| NoWaitOnNonLimit | a failure is noted as the limit only if the plan refuses that call's model | a subagent that failed for another reason sleeps until another model's reset | TestModelLimitEventIsNotEveryones |
| NoResumeOfFinished | a subagent is told to pick up only if its last run did not finish well | a finished subagent redoes or muddles its work | TestResumeSkipsSubagentThatFinished |
| NoEarlyResume | nothing resumes before the reset opt-chat was told plus grace | a resume that fails at once, burning a try | TestResumer |
| NoLateResume | no caller told its reset waits past its own time | a subagent waits days for Opt's weekly Opus reset | TestResumeEachAtItsOwnReset |
| NoLostResume | whoever the limit stopped is waiting, going out, or was told opt-chat gave up | work stops silently | TestResumeAtReset |
| TriesBounded | the count of resumes in a row stays bounded | resumes forever on a limit that never lifts | TestResumer |
| TimerOnTime | a pending resume always has its timer, no later than the earliest due | a resume that never goes out | TestResumer |

## Does a change touch it

The model's steps and the code they stand for:

| model | code |
|---|---|
| RefusedLocked | `refusedLocked` (limitresume.go) |
| NoteLimits | `noteLimits` (limits.go), fed by `proc.onLimits` (claude.go) |
| LimitHit, EndOther, EndLimit | `limitHitLocked` (limitresume.go); its callers agents.go (`e.IsError`) and main.go (turn `err`) |
| EndOk, Went | `limitWent`/`limitWentLocked`, `resumer.went` |
| Note | `resumer.note` |
| Due, Arm | `resumer.due`, `resumer.arm`, `resumeState.next` |
| Fire | `resumeAfterLimit` |
| Restart | `startResumer` reading resume.json; `refused` is not saved |

Sync commit: the model describes the code at **0769279**. To see if it
still does:

```sh
git diff 0769279 -- limitresume.go limits.go
git diff 0769279 -- agents.go main.go claude.go | grep -nE 'limitHit|limitWent|noteLimits|onLimits|resumes'
```

Empty output: the model still holds. Otherwise read the change against the
table above.

## Run it

```sh
formal/limitresume/check.sh      # about 2.5 min (MC.cfg) + 4 min (MC2.cfg) on 2 cores
formal/limitresume/mutants.sh    # about 2 min; every property must say "caught"
```

It needs Java 11 or later and curl; the first run fetches TLA+ tools 1.8.0
to `~/.cache/tla` and checks its sha256. A pass prints
`PASS <properties>: N distinct states found`. A failure prints
`FAIL <property>:` and the steps of the shortest counterexample;
`TRACE=1` prints every state.

Bounds: MC.cfg has one subagent, the clock 0..6, windows resetting by 6,
two run-outs, limitMax 2; MC2.cfg two subagents, the clock 0..3. Time is
whole steps, and grace, back-off and the fallback hour are one step each.
A pass covers these bounds, not every chat.

## Real bug or model error

Read the counterexample as a story in the code's words, one step to a line
(the table above names the code). Then ask of each step: can the code do
this, in this order? If yes, the code has a bug: write a Go test that fails
the same way, fix the code, mirror the fix in the model, and run both. If a
step cannot happen in the code, the model is wrong: fix the model to match
the code, and say so. Never weaken a property to make a run pass.

What the model leaves out: the regexp and parser; usage credits
(`overageStatus`) and `errorCode`; the few microseconds between `due`
unlocking and `resumeAfterLimit` reading who is at work (the model makes
them one step, so a subagent that ends in that gap and is told to pick up
goes unseen); a crash in that gap, which loses the resume by design (set
`CrashMidFire = TRUE` to see it); seven_day_sonnet, the mirror of the opus
case; more than two subagents.

## When the code changes on purpose

1. Mirror the change in LimitResume.tla, step for step.
2. Run check.sh. Every property passes, or the failure is a real finding.
3. Run mutants.sh. If a mutant's text no longer matches the spec, update
   that mutant so it still breaks its property.
4. Run the Go tests named above:
   `env -u ANTHROPIC_BASE_URL go test -short -run 'Limit|Resum' .`
5. Move the sync commit above forward, only after steps 2 to 4 pass at the
   new commit.
