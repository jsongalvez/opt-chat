---------------------------- MODULE LimitResume ----------------------------
(* The resume at a usage limit's reset, as limitresume.go, limits.go,       *)
(* main.go's turn loop and agents.go's result handling do it. Time is whole *)
(* steps; a step stands for any span (an hour, five minutes).               *)
EXTENDS Integers, FiniteSets, TLC

CONSTANTS
    Agents,       \* subagents; they run on Sonnet, Opt's turn on Opus
    MaxT,         \* the clock stops here
    MaxReset,     \* the latest a window can reset
    MaxHits,      \* how many times a window can run out
    Grace,        \* -reset-grace
    Backoff,      \* note's floor step (5 minutes in the code)
    LimitMax,     \* limitMax
    TriesWindow,  \* note's six hours since the last resume went out
    Fallback,     \* nextResetLocked's an hour from now
    CrashMidFire  \* may opt-chat stop between due's save and fire

None == -1
Zero == -100   \* time.Time{}: long before anything
Callers == {"opt"} \cup Agents
Family(c) == IF c = "opt" THEN "opus" ELSE "sonnet"
\* "shared" is five_hour or seven_day, which bind every model; "opus" is
\* seven_day_opus, which binds Opt's turn alone. seven_day_sonnet would be
\* the same case the other way round.
Windows == {"shared", "opus"}
Binding(c) == IF c = "opt" THEN {"shared", "opus"} ELSE {"shared"}

VARIABLES
    now,
    reset,     \* the plan: window -> when it resets; out while now < reset
    hits,
    refused,   \* Agent.refused: the last rate_limit_event that gave a status
    st,        \* resumer.st
    disk,      \* resume.json
    timer,     \* when resumer.timer fires, or None
    out,       \* the resumeState due took and fire has not yet handed on, else Empty
    state,     \* caller -> idle | running | stopped
    lastEnd,   \* caller -> how its last run ended: none | ok | limit | other
    told,      \* caller -> the reset opt-chat was told for it, or None
    own,       \* caller -> the latest resume time note worked out for it alone
    gaveUp,    \* callers note refused: limitMax in a row
    bad        \* the properties broken, by name

vars == <<now, reset, hits, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

Empty == [at |-> Zero, turn |-> FALSE, agents |-> {}, tries |-> 0, fired |-> Zero]
Pending(s) == s.turn \/ s.agents # {}
Who(s) == (IF s.turn THEN {"opt"} ELSE {}) \cup s.agents
Out(c) == {w \in Binding(c) : now < reset[w]}
PlanRefuses(c) == Out(c) # {}

\* refusedLocked: status and resetsAt only; the rate limit type is not read.
RefusedLocked == refused.status = "rejected" /\ refused.at > now

Init ==
    /\ now = 0
    /\ reset = [w \in Windows |-> 0]
    /\ hits = 0
    /\ refused = [status |-> "none", at |-> 0, type |-> "shared"]
    /\ st = Empty /\ disk = Empty
    /\ timer = None /\ out = Empty
    /\ state = [c \in Callers |-> "idle"]
    /\ lastEnd = [c \in Callers |-> "none"]
    /\ told = [c \in Callers |-> None]
    /\ own = [c \in Callers |-> None]
    /\ gaveUp = {} /\ bad = {}

\* A timer due goes off before time moves on, and fire follows due at once.
Tick ==
    /\ now < MaxT
    /\ ~(timer # None /\ timer <= now)
    /\ ~Pending(out)
    /\ now' = now + 1
    /\ UNCHANGED <<reset, hits, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

RunOut(w) ==
    /\ hits < MaxHits /\ now >= reset[w]
    /\ \E r \in now + 1 .. MaxReset : reset' = [reset EXCEPT ![w] = r]
    /\ hits' = hits + 1
    /\ UNCHANGED <<now, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

\* noteLimits: any running call's rate_limit_event replaces Agent.refused.
\* It tells the truth for that call's model; that it may not come at all is
\* what leaves an old one standing.
NoteLimits(c) ==
    /\ state[c] = "running"
    /\ IF PlanRefuses(c)
         THEN \E w \in Out(c) : refused' = [status |-> "rejected", at |-> reset[w], type |-> w]
         ELSE refused' = [status |-> "allowed", at |-> 0, type |-> "shared"]
    /\ UNCHANGED <<now, reset, hits, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

\* The user sends a message, or the user or Opt tells a subagent.
Start(c) ==
    /\ state[c] # "running"
    /\ state' = [state EXCEPT ![c] = "running"]
    /\ UNCHANGED <<now, reset, hits, refused, st, disk, timer, out, lastEnd, told, own, gaveUp, bad>>

\* resumer.note. r is the reset, informed whether opt-chat was told it.
Note(c, r, informed) ==
    LET tries == IF ~Pending(st) /\ now - st.fired > TriesWindow THEN 0 ELSE st.tries
        at0 == r + Grace
        floor == now + (tries + 1) * Backoff
        at == IF ~(r > now) /\ at0 < floor THEN floor ELSE at0
        s2 == [st EXCEPT !.tries = tries,
                         !.at = IF st.at < at THEN at ELSE st.at,
                         !.turn = st.turn \/ c = "opt",
                         !.agents = IF c = "opt" THEN st.agents ELSE st.agents \cup {c}]
    IN IF tries >= LimitMax
         THEN /\ gaveUp' = gaveUp \cup {c}
              /\ UNCHANGED <<st, disk, timer, own, told>>
         ELSE /\ st' = s2 /\ disk' = s2 /\ timer' = s2.at
              /\ own' = [own EXCEPT ![c] = IF c \in Who(st) /\ own[c] > at THEN own[c] ELSE at]
              /\ told' = [told EXCEPT ![c] = IF informed THEN r ELSE None]
              /\ gaveUp' = gaveUp \ {c}

\* limitHitLocked, for a call that failed; wt is the reset its message
\* names, or None.
LimitHit(c, words, wt) ==
    IF ~(RefusedLocked \/ words)
      THEN UNCHANGED <<st, disk, timer, own, told, gaveUp, bad>>
      ELSE /\ Note(c, IF wt # None THEN wt ELSE IF RefusedLocked THEN refused.at ELSE now + Fallback,
                   wt # None \/ RefusedLocked)
           /\ bad' = IF PlanRefuses(c) THEN bad ELSE bad \cup {"WaitsOnNonLimitFailure"}

\* went: a turn or a subagent finished without the limit.
Went ==
    IF st.tries > 0 /\ ~Pending(st)
      THEN st' = Empty /\ disk' = Empty
      ELSE UNCHANGED <<st, disk>>

EndOk(c) ==
    /\ state[c] = "running" /\ ~PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "idle"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "ok"]
    /\ Went
    /\ UNCHANGED <<now, reset, hits, refused, timer, out, told, own, gaveUp, bad>>

\* A failure that is not the limit: context, max turns, a crash.
EndOther(c) ==
    /\ state[c] = "running" /\ ~PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "stopped"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "other"]
    /\ LimitHit(c, FALSE, None)
    /\ UNCHANGED <<now, reset, hits, refused, out>>

\* The plan refuses the call: Claude Code's message names one window's
\* reset, or no time.
EndLimit(c) ==
    /\ state[c] = "running" /\ PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "stopped"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "limit"]
    /\ \E wt \in {None} \cup {reset[w] : w \in Out(c)} : LimitHit(c, TRUE, wt)
    /\ UNCHANGED <<now, reset, hits, refused, out>>

\* resumer.due: the timer went off.
Due ==
    /\ timer # None /\ timer <= now /\ ~Pending(out)
    /\ IF ~Pending(st)
         THEN /\ timer' = None /\ UNCHANGED <<st, disk, out, bad, told, own>>
         ELSE IF st.at > now
           THEN /\ timer' = st.at /\ UNCHANGED <<st, disk, out, bad, told, own>>
           ELSE /\ out' = st
                /\ bad' = IF \E c \in Who(st) : told[c] # None /\ now < told[c] + Grace
                           THEN bad \cup {"ResumesBeforeReset"} ELSE bad
                /\ st' = [Empty EXCEPT !.tries = st.tries + 1, !.fired = now]
                /\ disk' = st'
                /\ timer' = None
                /\ told' = [c \in Callers |-> None]
                /\ own' = [c \in Callers |-> None]
    /\ UNCHANGED <<now, reset, hits, refused, state, lastEnd, gaveUp>>

\* resumeAfterLimit: tell each stopped subagent not at work, and report,
\* which starts Opt's turn.
Fire ==
    /\ Pending(out)
    /\ LET back == {a \in out.agents : state[a] # "running"}
       IN /\ state' = [c \in Callers |-> IF c \in back \/ c = "opt" THEN "running" ELSE state[c]]
          /\ bad' = bad
                \cup (IF \E a \in back : lastEnd[a] # "limit" THEN {"ResumesFinishedSubagent"} ELSE {})
    /\ out' = Empty
    /\ UNCHANGED <<now, reset, hits, refused, st, disk, timer, lastEnd, told, own, gaveUp>>

\* opt-chat stops and starts again: Agent.refused is gone, resume.json is
\* read back and armed, and calls cut off go on (resume.go, turnend.go).
Restart ==
    /\ ~Pending(out) \/ CrashMidFire
    /\ refused' = [status |-> "none", at |-> 0, type |-> "shared"]
    /\ st' = disk
    /\ timer' = IF Pending(disk) THEN disk.at ELSE None
    /\ out' = Empty
    /\ UNCHANGED <<now, reset, hits, disk, state, lastEnd, told, own, gaveUp, bad>>

Next ==
    \/ Tick \/ Due \/ Fire \/ Restart
    \/ \E w \in Windows : RunOut(w)
    \/ \E c \in Callers : Start(c) \/ NoteLimits(c) \/ EndOk(c) \/ EndOther(c) \/ EndLimit(c)

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
\* Never waits on a failure that is not the plan's limit for that call.
NoWaitOnNonLimit == "WaitsOnNonLimitFailure" \notin bad

\* Never tells a subagent to pick up after it already finished.
NoResumeOfFinished == "ResumesFinishedSubagent" \notin bad

\* Never resumes before the reset opt-chat was told.
NoEarlyResume == "ResumesBeforeReset" \notin bad

\* The count stays in bounds, and a resume is never pending once spent.
TriesBounded == st.tries <= LimitMax /\ ~(Pending(st) /\ st.tries >= LimitMax)

\* A pending resume always has its timer set, no later than it is due.
TimerOnTime == Pending(st) => timer # None /\ timer <= st.at

\* No caller told its reset waits past the time worked out for it: another
\* caller's later reset does not hold it back.
NoLateResume == \A c \in Who(st) : told[c] # None => st.at <= own[c]

\* Whoever the limit stopped is pending, being handed on, or was told
\* opt-chat gave up.
NoLostResume ==
    \A c \in Callers :
        (state[c] = "stopped" /\ lastEnd[c] = "limit")
            => c \in Who(st) \/ c \in Who(out) \/ c \in gaveUp

Symmetry == Permutations(Agents)

=============================================================================
