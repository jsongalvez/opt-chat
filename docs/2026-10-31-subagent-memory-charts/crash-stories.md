## 1. Memory failed, nobody told

Rule broken: A failure of subagents' memory is said on the page, and zoom never says a subagent that has a log has none.
In the model, one step leads into this bug: "feeds its memory". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. The step is written to its log. Now: A: step on disk, memory behind
2. Feeds its memory. Now: Memory failed, nobody told

With the fix, step 2 (feeds its memory) takes you to: Every step in memory.

## 2. A's next step swallowed by a cut one

Rule broken: Every step written whole reads back as a step of its own.
In the model, one step leads into this bug: "the step is written to its log". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. Opt-chat crashes. Now: A's last write cut short
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
2. The step is written to its log. Now: A's next step swallowed by a cut one

With the fix, step 2 (the step is written to its log) takes you to: A: step on disk, memory behind.

## 3. A's next step swallowed by a cut one

Rule broken: A failure of subagents' memory is said on the page, and zoom never says a subagent that has a log has none.
In the model, one step leads into this bug: "feeds its memory". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. Opt-chat crashes. Now: A's last write cut short
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
2. The step is written to its log. Now: A's next step swallowed by a cut one
3. Feeds its memory. Now: A's next step swallowed by a cut one

With the fix, step 2 (the step is written to its log) takes you to: A: step on disk, memory behind.
