---- MODULE AgentMemLocks ----
\* AgentMem, scoped for a chart: B starts once A's chat is open, Opt zooms
\* A only, once. Both lock bugs stay in reach.
EXTENDS AgentMem
Scope == /\ steps["B"] > 0 => "A" \in open \/ crashes > 0
         /\ pc[Z] # "idle" => tgt[Z] = "A"
ChartRun == \/ \E s \in Subs : StartWrite(s) \/ WriteStep(s) \/ TornWrite(s) \/ Actor(W(s))
                               \/ BuildEnd(s) \/ BuildSc(s) \/ BuildIO(s)
           \/ StartZoom \/ Actor(Z) \/ View
           \/ StartPump \/ PumpNext \/ PumpSc
ChartNext == ChartRun \/ Crash
ChartSpec == Init /\ [][ChartNext]_vars
====
