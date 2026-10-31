---- MODULE AgentMemRestart ----
\* AgentMem, scoped for a chart: one subagent writing two steps, one crash
\* or failed write anywhere, one failed write to its chat, and each restart
\* turning -agent-memory either way. Opt zooms once.
EXTENDS AgentMem
ChartRun == \/ \E s \in Subs : StartWrite(s) \/ WriteStep(s) \/ TornWrite(s) \/ Actor(W(s))
                               \/ BuildEnd(s) \/ BuildSc(s) \/ BuildIO(s) \/ BuildFail(s)
           \/ StartZoom \/ Actor(Z) \/ View
           \/ StartPump \/ PumpNext \/ PumpSc
ChartNext == ChartRun \/ Crash \/ \E s \in Subs : Reuse(s)
ChartSpec == Init /\ [][ChartNext]_vars
====
