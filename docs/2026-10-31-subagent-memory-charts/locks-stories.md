## 1. A step waits on another subagent's disk

Rule broken: A subagent's step and Opt's zoom never wait on another subagent's disk.
Why: the pump holds the subagents' map while it waits for a chat.
In the model, 6 different steps lead into this bug: "the pump goes to the next chat", "takes the chat's lock to save it", "takes its chat's lock", "Opt zooms", "feeds its memory", and 1 more. The steps below are the shortest path that TLC found.

0. Start: Nothing written
1. A subagent starts a step. Now: Nothing written
2. Finds its entry. Now: Nothing written
3. Takes its own lock. Now: Nothing written
4. The step is written to its log. Now: A: step on disk, memory behind
5. Feeds its memory. Now: Every step in memory
6. Its memory opens. Now: Every step in memory
7. A subagent starts a step. Now: Every step in memory
8. Opt zooms. Now: Every step in memory
9. Finds its entry. Now: Every step in memory
10. Takes its own lock. Now: Every step in memory
11. Feeds its memory. Now: Every step in memory
12. Takes its chat's lock. Now: Every step in memory
13. A compaction ends: the pump runs. Now: Every step in memory
14. The pump goes to the next chat. Now: A step waits on another subagent's disk

With the fix, step 14 (the pump goes to the next chat) keeps you at: Every step in memory.

## 2. A step waits on another subagent's disk

Rule broken: A subagent's step and Opt's zoom never wait on another subagent's disk.
Why: closing idle memories holds the map while it waits for a chat.
In the model, 2 different steps lead into this bug: "closes idle memories" and "takes the chat's lock to save it". The steps below are the shortest path that TLC found.

0. Start: Nothing written
1. A subagent starts a step. Now: Nothing written
2. Finds its entry. Now: Nothing written
3. Takes its own lock. Now: Nothing written
4. The step is written to its log. Now: A: step on disk, memory behind
5. Feeds its memory. Now: Every step in memory
6. Its memory opens. Now: Every step in memory
7. A subagent starts a step. Now: Every step in memory
8. Finds its entry. Now: Every step in memory
9. Takes its own lock. Now: Every step in memory
10. The step is written to its log. Now: B: step on disk, memory behind
11. Feeds its memory. Now: Every step in memory
12. A compaction ends: the pump runs. Now: Every step in memory
13. The pump goes to the next chat. Now: Every step in memory
14. The pump takes a chat's lock. Now: Every step in memory
15. A summary comes back. Now: Every step in memory
16. Takes the chat's lock to save it. Now: Every step in memory
17. The pump goes to the next chat. Now: Every step in memory
18. Its memory opens. Now: Every step in memory
19. Closes idle memories. Now: A step waits on another subagent's disk

With the fix, step 19 (closes idle memories) keeps you at: Every step in memory.
