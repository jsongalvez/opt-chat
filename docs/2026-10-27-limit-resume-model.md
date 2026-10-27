# The resume at the reset, model-checked

`formal/limitresume/LimitResume.tla` models the resume at a usage limit's
reset (see [2026-10-23](2026-10-23-resume-at-reset.md)): the plan's windows
running out and resetting, rate_limit_events in any order or not at all,
turns and subagents ending well, at the limit or for another reason, the
user setting them to work again while they wait, the timer, and restarts.
TLC checks seven properties over every order of those steps within its
bounds. `formal/limitresume/check.sh` runs it; `mutants.sh` shows each
property can fail.

Run against the code as of 4bdfdba, it found three faults. A Go test now
shows each, failing before the fix and passing after.

- **A non-limit failure waited for another model's reset.** Opt's turn
  runs on Opus, subagents on Sonnet. An Opus turn's event refusing
  `seven_day_opus` was the last event when a Sonnet subagent failed for
  another reason, so that subagent waited days for the Opus reset.
  `refusedLocked` now counts only a window every model shares (`five_hour`,
  `seven_day`); a model's own window is told by the message's words.
  (`TestModelLimitEventIsNotEveryones`)
- **A subagent that had finished was told to pick up.** One the limit
  stopped, set to work again before the reset, that then finished, was
  still told at the reset to pick up where it stopped. Now a call that
  finishes takes its caller off the list (`resumer.went`); one that fails
  some other way leaves it waiting, to go on at the reset.
  (`TestResumeSkipsSubagentThatFinished`)
- **One caller's reset held up the rest.** resume.json kept one time for
  all, the latest. A subagent stopped by the five-hour window waited for
  Opt's weekly Opus reset, or Opt's turn for a subagent's. resume.json now
  keeps a time for each (`due`), and the timer goes off at the earliest.
  (`TestResumeEachAtItsOwnReset`)

Reading the parser for the model found a fourth. A reset named without a
date ("resets 4am", "resets Mon 4:00am") read just after that time was taken
as the next day's or the next week's. It now means that time if it passed
less than 15 minutes ago, and the back-off sets when to resume.
(`TestLimitResetJustPassed`)

The count of resumes in a row now counts each resume that goes out, as
callers may go out at different times. Those already waiting when it
reaches six still go out; the next limit sets none.

The model leaves out what each step does inside: the parser, the wording
regexp, Claude Code's own process, the fake's timing. Those are the Go
tests' part. It also leaves out the few microseconds between `due` letting
go of its lock and `resumeAfterLimit` reading which subagents are at work,
in which a subagent can end and still be told to pick up; and, unless
`CrashMidFire` is set, a crash in that gap, which loses that resume by
design (`due` saves first so a crash never repeats one).
