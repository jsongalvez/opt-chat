---------------------------- MODULE AgentMem ----------------------------
(* Subagents' memory (agentmem.go): each subagent's log fed into a chat of
   its own, zoomed, pumped, closed when idle, across a crash. Every action
   is one step of the code between two lock operations; its comment names
   the code. Fixes names the fixes made after b5cc5bb:
     "pump"   agentMems.pump copies the chats under m.mu, locks each without it
     "close"  closeIdle only tries a chat's mu
     "seal"   appendStep ends a log cut short with a blank line before a step
     "notice" a memory that fails to open says so on the page, and in zoom

   A log is a sequence of writes, each what appendStep's one write left:
     [t |-> "full", id |-> n]  a whole step, ending in a blank line
     [t |-> "head", id |-> n]  cut short after its head (a crash, a full disk)
     [t |-> "pre"]             cut short inside its head
     [t |-> "seal"]            a blank line alone *)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS Subs,      \* subagents' names
          MaxSteps,  \* steps each subagent writes
          MaxCrash,  \* crashes of opt-chat, or failed writes
          MaxFail,   \* compactions and opens that fail
          OpenMax,   \* agentOpenMax
          Fixes      \* which fixes the code has: {} is the code at b5cc5bb

NoLock == <<"", "">>                     \* no lock wanted
None == <<"none", "">>                   \* no holder; no zoom result yet
MM == <<"mm", "">>                       \* agentMems.mu
AM(s) == <<"am", s>>                     \* agentMem.mu
SC(s) == <<"sc", s>>                     \* the subagent chat's Chat.mu
Locks == {MM} \cup {AM(s) : s \in Subs} \cup {SC(s) : s \in Subs}

W(s) == <<"w", s>>                       \* the subagent's own goroutine: agentWriteRef
B(s) == <<"b", s>>                       \* a compaction of its chat ending: Chat.build
Z == <<"z", "">>                         \* Opt's zoom("Name"): ZoomAgent
P == <<"p", "">>                         \* jobGate.kick: agentMems.pump
Actors == {W(s) : s \in Subs} \cup {B(s) : s \in Subs} \cup {Z, P}

VARIABLES
  log,      \* agents/<Name>.log, on disk
  chat,     \* agents/<Name>.mem: the steps' ids, its messages' first parts, on disk
  read,     \* agentMem.read, in writes
  sealed,   \* agentMem.sealed (the fix): the log is known to end in a blank line
  ents,     \* agentMems.open: the names with an entry
  open,     \* those whose entry has its chat: am.c set
  busy,     \* a compaction of the chat underway (Chat.busy)
  pc, hold, \* each actor's place; each lock's holder
  tgt,      \* the subagent an actor works on: the zoom's, closeIdle's, pump's next
  plist,    \* the chats pump has left to visit
  steps, crashes, fails, nextId,
  noticed,  \* a memory failure said on the page (hub.notice)
  failed,   \* a memory failure happened
  zres      \* what the last zoom told Opt

vars == <<log, chat, read, sealed, ents, open, busy, pc, hold, tgt, plist, steps,
          crashes, fails, nextId, noticed, failed, zres>>

-------------------------------------------------------------------------
(* parseSteps (subs.go): a head starts a step only at the text's start or
   after a blank line; the steps' ids, in order. *)
RECURSIVE Parse(_, _)
Parse(q, ab) ==
  IF q = <<>> THEN <<>>
  ELSE LET h == Head(q) IN
    CASE h.t = "full" -> (IF ab THEN <<h.id>> ELSE <<>>) \o Parse(Tail(q), TRUE)
      [] h.t = "head" -> (IF ab THEN <<h.id>> ELSE <<>>) \o Parse(Tail(q), FALSE)
      [] h.t = "pre"  -> Parse(Tail(q), FALSE)
      [] h.t = "seal" -> Parse(Tail(q), TRUE)

\* feed's cut at the last blank line: bytes.LastIndex(b, "\n\n")
RECURSIVE Ends(_)
Ends(q) == IF q = <<>> THEN 0
           ELSE IF q[Len(q)].t \in {"full", "seal"} THEN Len(q)
           ELSE Ends(SubSeq(q, 1, Len(q) - 1))

Unread(s) == SubSeq(log[s], read[s] + 1, Len(log[s]))
Chunk(s) == SubSeq(Unread(s), 1, Ends(Unread(s)))
\* What feed logs: the chunk's steps, less those the chat holds when opened.
New(s) == LET st == Parse(Chunk(s), TRUE)
          IN IF read[s] = 0 THEN SubSeq(st, Len(chat[s]) + 1, Len(st)) ELSE st
Feed(s) == /\ chat' = [chat EXCEPT ![s] = @ \o New(s)]
           /\ read' = [read EXCEPT ![s] = @ + Len(Chunk(s))]

Subj(a) == IF a[1] \in {"w", "b"} THEN a[2] ELSE tgt[a]

WantOf(a) ==
  CASE pc[a] \in {"lock", "openm", "ci", "pm"} -> MM
    [] pc[a] = "am"    -> AM(Subj(a))
    [] pc[a] = "sc"    -> SC(Subj(a))
    [] pc[a] = "view"  -> SC(tgt[a])
    [] pc[a] = "bsc"   -> SC(a[2])
    [] pc[a] = "psc"   -> SC(tgt[a])
    [] pc[a] = "cisc" /\ "close" \notin Fixes -> SC(tgt[a])
    [] pc[a] = "ciam"  -> AM(tgt[a])
    [] OTHER -> NoLock

Free(l) == hold[l] = None
Take(a, ls) == hold' = [l \in Locks |-> IF l \in ls THEN a ELSE hold[l]]
Give(ls) == hold' = [l \in Locks |-> IF l \in ls THEN None ELSE hold[l]]
Go(a, p) == pc' = [pc EXCEPT ![a] = p]

-------------------------------------------------------------------------
(* agentMems.lock: m.mu held to find the entry, then the entry's mu. *)
Lock(a) == /\ pc[a] = "lock" /\ Free(MM) /\ Go(a, "am")
           /\ ents' = ents \cup {Subj(a)}
           /\ UNCHANGED <<log, chat, read, sealed, open, busy, hold, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
LockAm(a) == /\ pc[a] = "am" /\ Free(AM(Subj(a))) /\ Take(a, {AM(Subj(a))})
             /\ Go(a, IF a[1] = "w" THEN "append" ELSE "feed")
             /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>

\* Its own I/O on its log, holding its am.mu.
Written(s) == LET full == [t |-> "full", id |-> nextId]
                  ends == log[s] = <<>> \/ log[s][Len(log[s])].t \in {"full", "seal"}
              IN IF "seal" \in Fixes /\ ~sealed[s] /\ ~ends THEN <<[t |-> "seal"], full>> ELSE <<full>>

StartWrite(s) == /\ pc[W(s)] = "idle" /\ steps[s] < MaxSteps /\ Go(W(s), "lock")
                 /\ steps' = [steps EXCEPT ![s] = @ + 1]
                 /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, hold, tgt, plist, crashes, fails, nextId, noticed, failed, zres>>
WriteStep(s) == /\ pc[W(s)] = "append"
             /\ log' = [log EXCEPT ![s] = @ \o Written(s)]
             /\ sealed' = [sealed EXCEPT ![s] = TRUE]
             /\ nextId' = nextId + 1
             /\ Go(W(s), "feed")
             /\ UNCHANGED <<chat, read, ents, open, busy, hold, tgt, plist, steps, crashes, fails, noticed, failed, zres>>
\* appendRaw failing part way: a full disk. The step is lost, said in the terminal.
TornWrite(s) == /\ pc[W(s)] = "append" /\ crashes < MaxCrash
                /\ \E t \in {[t |-> "head", id |-> nextId], [t |-> "pre"]} :
                     log' = [log EXCEPT ![s] = Append(@, t)]
                /\ sealed' = [sealed EXCEPT ![s] = FALSE]
                /\ crashes' = crashes + 1 /\ nextId' = nextId + 1
                /\ Give({AM(s)}) /\ Go(W(s), "ci")
                /\ UNCHANGED <<chat, read, ents, open, busy, tgt, plist, steps, fails, noticed, failed, zres>>

(* agentMems.fed, am.mu held. An open chat: feed under its mu (logMsgs).
   None: openChat, feed it before anyone can see it, then m.mu and its mu
   to set am.c. openChat or a read can fail. *)
FeedStep(a) ==
  LET s == Subj(a) IN
  /\ pc[a] = "feed"
  /\ \/ /\ s \in open /\ Go(a, "sc")
        /\ UNCHANGED <<chat, read, failed, fails, noticed, zres, hold>>
     \/ /\ s \notin open /\ Feed(s) /\ Go(a, "openm")
        /\ UNCHANGED <<failed, fails, noticed, zres, hold>>
     \/ /\ s \notin open /\ fails < MaxFail
        /\ fails' = fails + 1 /\ failed' = TRUE
        \* appendStep: warn() to the terminal; ZoomAgent: "No subagent [Name]."
        /\ noticed' = ("notice" \in Fixes)
        /\ zres' = IF a = Z THEN (IF "notice" \notin Fixes THEN <<"no such subagent", "">> ELSE <<"error", "">>) ELSE zres
        /\ Give({AM(s)}) /\ Go(a, IF a = Z THEN "idle" ELSE "ci")
        /\ UNCHANGED <<chat, read>>
  /\ UNCHANGED <<log, sealed, ents, open, busy, tgt, plist, steps, crashes, nextId>>
LockSc(a) == /\ pc[a] = "sc" /\ Free(SC(Subj(a))) /\ Take(a, {SC(Subj(a))}) /\ Go(a, "logio")
             /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
\* logMsgs: appendDay and saveView fsync with the chat's mu held.
LogIO(a) == LET s == Subj(a) IN
            /\ pc[a] = "logio" /\ Feed(s) /\ Give({SC(s), AM(s)}) /\ Go(a, IF a = Z THEN "view" ELSE "ci")
            /\ UNCHANGED <<log, sealed, ents, open, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
OpenSet(a) == LET s == Subj(a) IN
              /\ pc[a] = "openm" /\ Free(MM)
              /\ open' = open \cup {s}
              /\ Give({AM(s)}) /\ Go(a, IF a = Z THEN "view" ELSE "ci")
              /\ UNCHANGED <<log, chat, read, sealed, ents, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>

(* closeIdle, after am.mu is given back: m.mu over the whole pass. *)
CloseIdle(a) ==
  /\ pc[a] = "ci" /\ Free(MM)
  /\ IF Cardinality(ents) <= OpenMax \/ ents \subseteq {Subj(a)}
       THEN Go(a, "idle") /\ UNCHANGED <<hold, tgt>>
       ELSE \E t \in ents \ {Subj(a)} :
              IF Free(AM(t))       \* am.mu.TryLock
                THEN Take(a, {MM, AM(t)}) /\ tgt' = [tgt EXCEPT ![a] = t] /\ Go(a, "cisc")
                ELSE Go(a, "idle") /\ UNCHANGED <<hold, tgt>>
  /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
\* am.mu.Lock() in place of TryLock: what the lock order forbids. No code
\* takes this step; mutants.sh puts it in to show NoDeadlock can fail.
CloseAm(a) == /\ pc[a] = "ciam" /\ Free(AM(tgt[a])) /\ Take(a, {AM(tgt[a])}) /\ Go(a, "cisc")
              /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
\* sc.mu.Lock(); with "close", sc.mu.TryLock(), as for am.mu.
CloseSc(a) ==
  LET t == tgt[a] IN
  /\ pc[a] = "cisc" /\ ("close" \notin Fixes => Free(SC(t)))
  /\ IF Free(SC(t)) /\ ~busy[t]
       THEN ents' = ents \ {t} /\ open' = open \ {t} /\ read' = [read EXCEPT ![t] = 0] /\ sealed' = [sealed EXCEPT ![t] = FALSE]
       ELSE UNCHANGED <<ents, open, read, sealed>>
  /\ Give({MM, AM(t)}) /\ Go(a, "idle")
  /\ UNCHANGED <<log, chat, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>

(* ZoomAgent. *)
StartZoom == /\ pc[Z] = "idle" /\ zres = None
             /\ \E s \in Subs : log[s] # <<>> /\ tgt' = [tgt EXCEPT ![Z] = s]
             /\ Go(Z, "lock")
             /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, hold, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
View == /\ pc[Z] = "view" /\ Free(SC(tgt[Z])) /\ Go(Z, "idle") /\ zres' = <<"view", "">>
        /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, hold, tgt, plist, steps, crashes, fails, nextId, noticed, failed>>

(* agentMems.pump: m.mu over every chat's mu; with "pump", the chats
   copied under m.mu and their mus taken without it. *)
StartPump == /\ pc[P] = "idle" /\ open # {} /\ (\E s \in open : ~busy[s] /\ chat[s] # <<>>)
             /\ Free(MM) /\ plist' = open
             /\ IF "pump" \notin Fixes THEN Take(P, {MM}) ELSE UNCHANGED hold
             /\ Go(P, "pnext")
             /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, tgt, steps, crashes, fails, nextId, noticed, failed, zres>>
PumpNext == /\ pc[P] = "pnext"
            /\ IF plist = {}
                 THEN Go(P, "idle") /\ (IF "pump" \notin Fixes THEN Give({MM}) ELSE UNCHANGED hold) /\ UNCHANGED <<tgt, plist>>
                 ELSE \E t \in plist : tgt' = [tgt EXCEPT ![P] = t] /\ plist' = plist \ {t} /\ Go(P, "psc") /\ UNCHANGED hold
            /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, steps, crashes, fails, nextId, noticed, failed, zres>>
\* Chat.pump under its mu: a compaction starts (jobGate.take).
PumpSc == LET t == tgt[P] IN
          /\ pc[P] = "psc" /\ Free(SC(t)) /\ Go(P, "pnext")
          /\ busy' = [busy EXCEPT ![t] = @ \/ (t \in open /\ chat[t] # <<>>)]
          /\ UNCHANGED <<log, chat, read, sealed, ents, open, hold, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>

(* Chat.build: the model's call without locks, then its mu over writing
   the line (flushNodes, saveView: fsync). A failure warns (subChat.warn). *)
BuildEnd(s) == /\ pc[B(s)] = "idle" /\ busy[s] /\ Go(B(s), "bsc")
               /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, hold, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
BuildSc(s) == /\ pc[B(s)] = "bsc" /\ Free(SC(s)) /\ Take(B(s), {SC(s)}) /\ Go(B(s), "bio")
              /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, tgt, plist, steps, crashes, fails, nextId, noticed, failed, zres>>
BuildIO(s) == /\ pc[B(s)] = "bio" /\ Give({SC(s)}) /\ Go(B(s), "idle")
              /\ busy' = [busy EXCEPT ![s] = FALSE]
              /\ \/ UNCHANGED <<fails, failed, noticed>>
                 \/ fails < MaxFail /\ fails' = fails + 1 /\ failed' = TRUE /\ noticed' = TRUE
              /\ UNCHANGED <<log, chat, read, sealed, ents, open, tgt, plist, steps, crashes, nextId, zres>>

(* opt-chat dies (or /reload execs): what is on disk stays. A crash in a
   write leaves it cut short. *)
Crash == /\ crashes < MaxCrash /\ crashes' = crashes + 1
         /\ \E s \in Subs, cut \in {<<>>, <<[t |-> "head", id |-> nextId]>>, <<[t |-> "pre"]>>} :
              /\ cut # <<>> => pc[W(s)] = "append"
              /\ log' = [log EXCEPT ![s] = @ \o cut]
         /\ nextId' = nextId + 1
         /\ read' = [s \in Subs |-> 0] /\ sealed' = [s \in Subs |-> FALSE]
         /\ ents' = {} /\ open' = {} /\ busy' = [s \in Subs |-> FALSE]
         /\ pc' = [a \in Actors |-> "idle"] /\ hold' = [l \in Locks |-> None]
         /\ plist' = {}
         /\ UNCHANGED <<chat, tgt, steps, fails, noticed, failed, zres>>

ZoomAgain == /\ pc[Z] = "idle" /\ zres # None /\ zres' = None
             /\ UNCHANGED <<log, chat, read, sealed, ents, open, busy, pc, hold, tgt, plist, steps, crashes, fails, nextId, noticed, failed>>

Init == /\ log = [s \in Subs |-> <<>>] /\ chat = [s \in Subs |-> <<>>]
        /\ read = [s \in Subs |-> 0] /\ sealed = [s \in Subs |-> FALSE]
        /\ ents = {} /\ open = {} /\ busy = [s \in Subs |-> FALSE]
        /\ pc = [a \in Actors |-> "idle"] /\ hold = [l \in Locks |-> None]
        /\ tgt = [a \in Actors |-> CHOOSE s \in Subs : TRUE] /\ plist = {}
        /\ steps = [s \in Subs |-> 0] /\ crashes = 0 /\ fails = 0 /\ nextId = 1
        /\ noticed = FALSE /\ failed = FALSE /\ zres = None

Actor(a) == Lock(a) \/ LockAm(a) \/ FeedStep(a) \/ LockSc(a) \/ LogIO(a) \/ OpenSet(a)
            \/ CloseIdle(a) \/ CloseAm(a) \/ CloseSc(a)
Next == \/ \E s \in Subs : StartWrite(s) \/ WriteStep(s) \/ TornWrite(s) \/ Actor(W(s))
                           \/ BuildEnd(s) \/ BuildSc(s) \/ BuildIO(s)
        \/ StartZoom \/ Actor(Z) \/ View \/ ZoomAgain
        \/ StartPump \/ PumpNext \/ PumpSc
        \/ Crash

Spec == Init /\ [][Next]_vars

\* States equal but for whom an idle actor last worked on are one state.
StateView == <<log, chat, read, sealed, ents, open, busy, pc, hold, plist, steps, crashes, fails,
               nextId, noticed, failed, zres, [a \in Actors |-> IF pc[a] = "idle" THEN "" ELSE tgt[a]]>>

-------------------------------------------------------------------------
Blocked(a) == WantOf(a) # NoLock /\ hold[WantOf(a)] \notin {None, a}
RECURSIVE Waits(_, _)
\* Whom a waits on, lock by lock.
Waits(a, n) == IF n = 0 \/ ~Blocked(a) THEN {}
               ELSE LET h == hold[WantOf(a)] IN {h} \cup Waits(h, n - 1)
InIO(a) == pc[a] \in {"append", "logio", "bio"} \/ (pc[a] = "feed" /\ Subj(a) \notin open)

\* A subagent's step and Opt's zoom never wait on another subagent's disk.
NoWaitOnOthersIO == \A a \in {W(s) : s \in Subs} \cup {Z} :
                      \A h \in Waits(a, Cardinality(Actors)) : InIO(h) => Subj(h) = Subj(a)
\* No two wait on each other: the lock order holds.
NoDeadlock == \A a \in Actors : a \notin Waits(a, Cardinality(Actors))
\* The chat holds the log's steps, in order, none twice: it is always a
\* first part of what the whole log reads as.
ChatIsLog == \A s \in Subs : LET all == Parse(SubSeq(log[s], 1, Ends(log[s])), TRUE) IN
               Len(chat[s]) <= Len(all) /\ chat[s] = SubSeq(all, 1, Len(chat[s]))
\* Every step written whole reads back as a step of its own.
NoLostStep == \A s \in Subs : \A i \in 1..Len(log[s]) :
                log[s][i].t = "full" =>
                  \E j \in 1..Len(Parse(log[s], TRUE)) : Parse(log[s], TRUE)[j] = log[s][i].id
\* A failure of subagents' memory is said on the page, and zoom never says
\* a subagent that has a log has none.
FailureSurfaces == /\ failed => noticed
                   /\ zres # <<"no such subagent", "">>
=========================================================================
