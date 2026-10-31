## 1. A's next step swallowed by a cut one

Rule broken: Every step written whole reads back as a step of its own.
In the model, one step leads into this bug: "the step is written to its log". The steps below are the shortest path that TLC found.

0. Start: Memory off: steps go to the log alone
   (1 hidden step: a subagent starts a step)
1. The write is cut short. Now: A's last write cut short
   (1 hidden step: a subagent starts a step)
2. The step is written to its log. Now: A's next step swallowed by a cut one

With the fix, step 2 (the step is written to its log) takes you to: Memory off: steps go to the log alone.

## 2. opt-chat stopped by a subagent's memory

Rule broken: A failure in a subagent's memory never stops opt-chat.
In the model, 2 different steps lead into this bug: "saving the summary fails (a full disk)" and "writing its chat fails (a full disk)". The steps below are the shortest path that TLC found.

0. Start: Memory off: steps go to the log alone
   (3 hidden steps: a subagent starts a step, the step is written to its log)
1. Opt-chat crashes or restarts, memory on or off. Now: A's last write cut short
   (10 hidden steps: Opt zooms, finds its entry, takes its own lock, feeds its memory, its memory opens, a compaction ends: the pump runs, the pump goes to the next chat, the pump takes a chat's lock, a summary comes back, takes the chat's lock to save it)
2. Saving the summary fails (a full disk). Now: opt-chat stopped by a subagent's memory

With the fix, step 2 (saving the summary fails (a full disk)) keeps you at: A's last write cut short.

## 3. A's memory differs from its log

Rule broken: The chat holds the log's steps, in order, none twice, message by message: it is always a first part of what the whole log reads as.
In the model, one step leads into this bug: "its log is deleted, a new subagent takes the name". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. The step is written to its log. Now: A: step on disk, memory behind
2. Feeds its memory. Now: Every step in memory
   (2 hidden steps: its memory opens, closes idle memories)
3. Its log is deleted, a new subagent takes the name. Now: A's memory differs from its log

With the fix, step 3 (its log is deleted, a new subagent takes the name) cannot happen, so you stay at: Every step in memory.

## 4. opt-chat stopped by a subagent's memory

Rule broken: The chat holds the log's steps, in order, none twice, message by message: it is always a first part of what the whole log reads as.
In the model, one step leads into this bug: "its log is deleted, a new subagent takes the name". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. The step is written to its log. Now: A: step on disk, memory behind
2. Feeds its memory. Now: Every step in memory
   (6 hidden steps: its memory opens, a compaction ends: the pump runs, the pump goes to the next chat, the pump takes a chat's lock, a summary comes back, takes the chat's lock to save it)
3. Saving the summary fails (a full disk). Now: opt-chat stopped by a subagent's memory
4. Its log is deleted, a new subagent takes the name. Now: opt-chat stopped by a subagent's memory

With the fix, step 3 (saving the summary fails (a full disk)) takes you to: Every step in memory, a failure said.

## 5. opt-chat stopped by a subagent's memory

Rule broken: Whatever crashed where, and whichever way the restarts turned the off switch: once fed to the log's end, an open chat is the whole log, as if nothing had happened; and zoom never shows a step cut short.
In the model, one step leads into this bug: "its memory opens". The steps below are the shortest path that TLC found.

0. Start: Memory off: steps go to the log alone
   (1 hidden step: a subagent starts a step)
1. Opt-chat crashes or restarts, memory on or off. Now: A's last write cut short
   (6 hidden steps: a subagent starts a step, finds its entry, Opt zooms, takes its own lock, feeds its memory)
2. Its memory opens. Now: A's last write cut short
   (1 hidden step: takes its own lock)
3. The step is written to its log. Now: A: step on disk, memory behind
   (2 hidden steps: feeds its memory, takes its chat's lock)
4. Writing its chat fails (a full disk). Now: opt-chat stopped by a subagent's memory
   (3 hidden steps: Opt zooms, finds its entry, takes its own lock)
5. Feeds its memory. Now: opt-chat stopped by a subagent's memory
6. Its memory opens. Now: opt-chat stopped by a subagent's memory

With the fix, step 4 (writing its chat fails (a full disk)) keeps you at: A: step on disk, memory behind.

## 6. A's memory differs from its log

Rule broken: Whatever crashed where, and whichever way the restarts turned the off switch: once fed to the log's end, an open chat is the whole log, as if nothing had happened; and zoom never shows a step cut short.
In the model, one step leads into this bug: "its memory opens". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. The step is written to its log. Now: A: step on disk, memory behind
2. Feeds its memory. Now: Every step in memory
3. Its memory opens. Now: Every step in memory
   (1 hidden step: closes idle memories)
4. Its log is deleted, a new subagent takes the name. Now: A's memory differs from its log
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
5. The step is written to its log. Now: A's memory differs from its log
6. Feeds its memory. Now: A's memory differs from its log
7. Its memory opens. Now: A's memory differs from its log

With the fix, step 4 (its log is deleted, a new subagent takes the name) cannot happen, so you stay at: Every step in memory.

## 7. A's memory open, part of the log missing

Rule broken: Whatever crashed where, and whichever way the restarts turned the off switch: once fed to the log's end, an open chat is the whole log, as if nothing had happened; and zoom never shows a step cut short.
In the model, one step leads into this bug: "its memory opens". The steps below are the shortest path that TLC found.

0. Start: Nothing written
   (3 hidden steps: a subagent starts a step, finds its entry, takes its own lock)
1. The step is written to its log. Now: A: step on disk, memory behind
2. Feeds its memory. Now: Every step in memory
3. Its memory opens. Now: Every step in memory
   (4 hidden steps: closes idle memories, a subagent starts a step, finds its entry, takes its own lock)
4. The step is written to its log. Now: A: step on disk, memory behind
   (2 hidden steps: feeds its memory, takes its chat's lock)
5. Opt-chat crashes or restarts, memory on or off. Now: A's memory cut mid-step
   (3 hidden steps: Opt zooms, finds its entry, takes its own lock)
6. Feeds its memory. Now: A's memory cut mid-step
7. Its memory opens. Now: A's memory open, part of the log missing

With the fix, step 7 (its memory opens) cannot happen, so you stay at: A's memory cut mid-step.
