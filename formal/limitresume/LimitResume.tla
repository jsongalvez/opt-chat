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
Callers == {"opt"} \cup Agents   \* "opt" is Opt's turn, "" in the code
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
    out,       \* the resumeState due took and fire has not yet handed on
    state,     \* caller -> idle | running | stopped
    lastEnd,   \* caller -> how its last run ended: none | ok | limit | other
    told,      \* caller -> the reset opt-chat was told for it, or None
    own,       \* caller -> the latest resume time note worked out for it
    gaveUp,    \* callers note refused: limitMax in a row
    bad        \* the properties broken, by name

vars == <<now, reset, hits, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

NoDue == [c \in Callers |-> None]
Empty == [due |-> NoDue, tries |-> 0, fired |-> Zero]
Who(s) == {c \in Callers : s.due[c] # None}
Pending(s) == Who(s) # {}
\* resumeState.next
Next(s) == CHOOSE t \in {s.due[c] : c \in Who(s)} : \A c \in Who(s) : t <= s.due[c]
Arm(s) == IF Pending(s) THEN Next(s) ELSE None
Out(c) == {w \in Binding(c) : now < reset[w]}
PlanRefuses(c) == Out(c) # {}

\* refusedLocked: a window every model shares, not past its reset. Usage
\* credits and errorCode are left out: the model has none.
RefusedLocked == refused.status = "rejected" /\ refused.type = "shared" /\ refused.at > now

Init ==
    /\ now = 0
    /\ reset = [w \in Windows |-> 0]
    /\ hits = 0
    /\ refused = [status |-> "none", at |-> 0, type |-> "shared"]
    /\ st = Empty /\ disk = Empty
    /\ timer = None /\ out = Empty
    /\ state = [c \in Callers |-> "idle"]
    /\ lastEnd = [c \in Callers |-> "none"]
    /\ told = NoDue /\ own = NoDue
    /\ gaveUp = {} /\ bad = {}

\* due calls fire as it lets go of the lock; the model lets nothing but a
\* crash come between. The code leaves a few microseconds in which a
\* subagent can end, then be told to pick up.
Calm == ~Pending(out)

\* A timer due goes off before time moves on.
Tick ==
    /\ now < MaxT
    /\ ~(timer # None /\ timer <= now)
    /\ Calm
    /\ now' = now + 1
    /\ UNCHANGED <<reset, hits, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

RunOut(w) ==
    /\ Calm
    /\ hits < MaxHits /\ now >= reset[w]
    /\ \E r \in now + 1 .. MaxReset : reset' = [reset EXCEPT ![w] = r]
    /\ hits' = hits + 1
    /\ UNCHANGED <<now, refused, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

\* noteLimits: any running call's rate_limit_event replaces Agent.refused.
\* It tells the truth for that call's model; that it may not come at all is
\* what leaves an old one standing.
NoteLimits(c) ==
    /\ Calm
    /\ state[c] = "running"
    /\ IF PlanRefuses(c)
         THEN \E w \in Out(c) : refused' = [status |-> "rejected", at |-> reset[w], type |-> w]
         ELSE refused' = [status |-> "allowed", at |-> 0, type |-> "shared"]
    /\ UNCHANGED <<now, reset, hits, st, disk, timer, out, state, lastEnd, told, own, gaveUp, bad>>

\* The user sends a message, or the user or Opt tells a subagent.
Start(c) ==
    /\ Calm
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
                         !.due[c] = IF st.due[c] # None /\ st.due[c] >= at THEN st.due[c] ELSE at]
    IN IF tries >= LimitMax
         THEN /\ gaveUp' = gaveUp \cup {c}
              /\ UNCHANGED <<st, disk, timer, own, told>>
         ELSE /\ st' = s2 /\ disk' = s2 /\ timer' = Arm(s2)
              /\ own' = [own EXCEPT ![c] = IF own[c] > at THEN own[c] ELSE at]
              /\ told' = [told EXCEPT ![c] = IF informed THEN r ELSE None]
              /\ gaveUp' = gaveUp \ {c}

\* resumer.went: c's call finished without the limit.
Went(c) ==
    IF st.due[c] = None /\ st.tries = 0
      THEN UNCHANGED <<st, disk, timer, told, own>>
      ELSE LET s1 == [st EXCEPT !.due[c] = None, !.tries = 0]
               s2 == IF ~Pending(s1) THEN Empty ELSE s1
           IN /\ st' = s2 /\ disk' = s2 /\ timer' = Arm(s2)
              /\ told' = [told EXCEPT ![c] = None]
              /\ own' = [own EXCEPT ![c] = None]

\* limitHitLocked, for a call that failed; wt is the reset its message
\* names, or None.
LimitHit(c, words, wt) ==
    IF ~(RefusedLocked \/ words)
      THEN UNCHANGED <<st, disk, timer, own, told, gaveUp, bad>>
      ELSE /\ Note(c, IF wt # None THEN wt ELSE IF RefusedLocked THEN refused.at ELSE now + Fallback,
                   wt # None \/ RefusedLocked)
           /\ bad' = IF PlanRefuses(c) THEN bad ELSE bad \cup {"WaitsOnNonLimitFailure"}

EndOk(c) ==
    /\ Calm
    /\ state[c] = "running" /\ ~PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "idle"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "ok"]
    /\ Went(c)
    /\ UNCHANGED <<now, reset, hits, refused, out, gaveUp, bad>>

\* A failure that is not the limit: context, max turns, a crash.
EndOther(c) ==
    /\ Calm
    /\ state[c] = "running" /\ ~PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "stopped"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "other"]
    /\ LimitHit(c, FALSE, None)
    /\ UNCHANGED <<now, reset, hits, refused, out>>

\* The plan refuses the call: Claude Code's message names one window's
\* reset, or no time.
EndLimit(c) ==
    /\ Calm
    /\ state[c] = "running" /\ PlanRefuses(c)
    /\ state' = [state EXCEPT ![c] = "stopped"]
    /\ lastEnd' = [lastEnd EXCEPT ![c] = "limit"]
    /\ \E wt \in {None} \cup {reset[w] : w \in Out(c)} : LimitHit(c, TRUE, wt)
    /\ UNCHANGED <<now, reset, hits, refused, out>>

\* resumer.due: the timer went off; it takes those whose time has come.
Due ==
    /\ timer # None /\ timer <= now /\ Calm
    /\ LET taken == {c \in Who(st) : st.due[c] <= now}
           rest == [st EXCEPT !.due = [c \in Callers |-> IF c \in taken THEN None ELSE st.due[c]],
                              !.tries = st.tries + 1, !.fired = now]
       IN IF taken = {}
            THEN /\ timer' = Arm(st)
                 /\ UNCHANGED <<st, disk, out, told, own, bad>>
            ELSE /\ out' = [st EXCEPT !.due = [c \in Callers |-> IF c \in taken THEN st.due[c] ELSE None]]
                 /\ st' = rest /\ disk' = rest
                 /\ timer' = Arm(rest)
                 /\ bad' = IF \E c \in taken : told[c] # None /\ now < told[c] + Grace
                             THEN bad \cup {"ResumesBeforeReset"} ELSE bad
                 /\ told' = [c \in Callers |-> IF c \in taken THEN None ELSE told[c]]
                 /\ own' = [c \in Callers |-> IF c \in taken THEN None ELSE own[c]]
    /\ UNCHANGED <<now, reset, hits, refused, state, lastEnd, gaveUp>>

\* resumeAfterLimit: tell each stopped subagent not at work, and report,
\* which starts Opt's turn.
Fire ==
    /\ Pending(out)
    /\ LET back == {a \in Who(out) \ {"opt"} : state[a] # "running"}
       IN /\ state' = [c \in Callers |-> IF c \in back \/ c = "opt" THEN "running" ELSE state[c]]
          /\ bad' = IF \E a \in back : lastEnd[a] = "ok"
                      THEN bad \cup {"ResumesFinishedSubagent"} ELSE bad
    /\ out' = Empty
    /\ UNCHANGED <<now, reset, hits, refused, st, disk, timer, lastEnd, told, own, gaveUp>>

\* opt-chat stops and starts again: Agent.refused is gone, resume.json is
\* read back and armed, and calls cut off go on (resume.go, turnend.go).
Restart ==
    /\ ~Pending(out) \/ CrashMidFire
    /\ refused' = [status |-> "none", at |-> 0, type |-> "shared"]
    /\ st' = disk
    /\ timer' = Arm(disk)
    /\ out' = Empty
    /\ UNCHANGED <<now, reset, hits, disk, state, lastEnd, told, own, gaveUp, bad>>

Step ==
    \/ Tick \/ Due \/ Fire \/ Restart
    \/ \E w \in Windows : RunOut(w)
    \/ \E c \in Callers : Start(c) \/ NoteLimits(c) \/ EndOk(c) \/ EndOther(c) \/ EndLimit(c)

Spec == Init /\ [][Step]_vars

-----------------------------------------------------------------------------
\* Never waits on a failure that is not the plan's limit for that call.
NoWaitOnNonLimit == "WaitsOnNonLimitFailure" \notin bad

\* Never tells a subagent to pick up after it already finished. One whose
\* last run failed some other way may be told: it has not finished.
NoResumeOfFinished == "ResumesFinishedSubagent" \notin bad

\* Never resumes before the reset opt-chat was told.
NoEarlyResume == "ResumesBeforeReset" \notin bad

\* No caller told its reset waits past the time worked out for it: another
\* caller's later reset does not hold it back.
NoLateResume == \A c \in Who(st) : told[c] # None => st.due[c] <= own[c]

\* Whoever the limit stopped is pending, being handed on, or was told
\* opt-chat gave up.
NoLostResume ==
    \A c \in Callers :
        (state[c] = "stopped" /\ lastEnd[c] = "limit")
            => c \in Who(st) \cup Who(out) \cup gaveUp

\* The count stays in bounds: note sets nothing once it reaches LimitMax,
\* and each caller waiting then goes out once more at most.
TriesBounded == st.tries <= LimitMax + Cardinality(Callers)

\* A pending resume always has its timer set, no later than it is due.
TimerOnTime == Pending(st) => timer # None /\ timer <= Next(st)

Symmetry == Permutations(Agents)
=============================================================================
