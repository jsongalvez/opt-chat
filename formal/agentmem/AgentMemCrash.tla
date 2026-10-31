---- MODULE AgentMemCrash ----
\* AgentMem, scoped for a chart: one subagent, a crash or a failed write,
\* a memory that fails to open; Opt zooms once.
EXTENDS AgentMem
ChartRun == \/ \E s \in Subs : StartWrite(s) \/ WriteStep(s) \/ TornWrite(s) \/ Actor(W(s))
                               \/ BuildEnd(s) \/ BuildSc(s) \/ BuildIO(s)
           \/ StartZoom \/ Actor(Z) \/ View
           \/ StartPump \/ PumpNext \/ PumpSc
ChartNext == ChartRun \/ Crash
ChartSpec == Init /\ [][ChartNext]_vars
====
