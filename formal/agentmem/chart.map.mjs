// Chart labels for AgentMem (formal/agentmem), for /statechart: AgentMemLocks.tla, AgentMemCrash.tla and AgentMemRestart.tla.
const k = (a) => JSON.stringify(a);
const MM = k(["mm", ""]);
const subj = (v, a) => (a[0] === "w" || a[0] === "b" ? a[1] : v.tgt[k(a)]);
const who = (a) => (a[0] === "w" ? `${a[1]}'s step` : a[0] === "z" ? "Opt's zoom" : a[0] === "b" ? `${a[1]}'s summary` : "the pump");

function want(v, a, facts) {
  const pc = v.pc[k(a)], s = subj(v, a), t = v.tgt[k(a)];
  if (["lock", "openm", "ci", "pm"].includes(pc)) return MM;
  if (pc === "am") return k(["am", s]);
  if (pc === "sc") return k(["sc", s]);
  if (pc === "view" || pc === "psc") return k(["sc", t]);
  if (pc === "bsc") return k(["sc", a[1]]);
  if (pc === "ciam") return k(["am", t]);
  if (pc === "cisc" && !facts.enabled.has("CloseSc")) return k(["sc", t]);
  return null;
}
const inIO = (v, a) => {
  const pc = v.pc[k(a)];
  return ["append", "logio", "bio"].includes(pc) || (pc === "feed" && !v.open.includes(subj(v, a)));
};
// Whom a waits on, holder by holder, as Waits in the spec.
function holders(v, a, facts, seen = new Set()) {
  const l = want(v, a, facts);
  if (!l) return [];
  const t = v.hold[l];
  const h = [t[1], t[2]];
  if (!h || h[0] === "none" || k(h) === k(a) || seen.has(k(h))) return [];
  seen.add(k(h));
  return [h, ...holders(v, h, facts, seen)];
}

const fulls = (log) => log.filter((t) => t.t === "full").length;
const cut = (log) => log.length > 0 && !["full", "seal"].includes(log[log.length].t);

// parseSteps (subs.go): a head starts a step only at the start or after a blank line.
function parse(log) {
  const out = [];
  let blank = true;
  for (const t of log) {
    if ((t.t === "full" || t.t === "head") && blank) out.push(t.id);
    blank = t.t === "full" || t.t === "seal";
  }
  return out;
}

// A step's messages in a chat, as the spec's Parts: an even id has two.
const parts = (id) => (id % 2 === 0 ? 2 : 1);
const expand = (ids) => ids.flatMap((id) => (parts(id) === 2 ? [k([id, 0]), k([id, 1])] : [k([id, 0])]));
const ends = (log) => {
  let n = log.length;
  while (n > 0 && !["full", "seal"].includes(log[n].t)) n--;
  return n;
};
const held = (chat) => [...chat].filter((m) => m.p === 0).length;
function wrongChat(chat, log) {
  const all = expand(parse([...log].slice(0, ends(log))));
  const got = [...chat].map((m) => k([m.id, m.p]));
  return got.length > all.length || got.some((m, i) => m !== all[i]);
}
const cutMidStep = (chat) => chat.length > 0 && chat[chat.length].p + 1 < parts(chat[chat.length].id);

export const readerActions = [];

export function state(v, facts) {
  const subs = Object.keys(v.log).sort();
  for (const a of Object.keys(v.pc).map((x) => JSON.parse(x))) {
    if (a[0] !== "w" && a[0] !== "z") continue;
    const io = holders(v, a, facts).find((h) => inIO(v, h) && subj(v, h) !== subj(v, a));
    if (io) return a[0] === "z" ? "Opt's zoom waits on another subagent's disk" : "A step waits on another subagent's disk";
  }
  for (const s of subs) {
    const parsed = parse(v.log[s]);
    if (v.log[s].some((t) => t.t === "full" && !parsed.includes(t.id))) return `${s}'s next step swallowed by a cut one`;
  }
  if (v.killed) return "opt-chat stopped by a subagent's memory";
  for (const s of subs) if (wrongChat(v.chat[s], v.log[s])) return `${s}'s memory differs from its log`;
  if (v.zres[1] === "view" && v.zres[2] === "torn") return "Opt read a step cut short";
  for (const s of subs)
    if (v.open.includes(s) && v.read[s] === ends(v.log[s]) && [...v.chat[s]].length < expand(parse([...v.log[s]].slice(0, ends(v.log[s])))).length)
      return `${s}'s memory open, part of the log missing`;
  if (v.failed && !v.noticed) return "Memory failed, nobody told";
  for (const s of subs) if (cut(v.log[s])) return `${s}'s last write cut short`;
  for (const s of subs) if (cutMidStep(v.chat[s])) return `${s}'s memory cut mid-step`;
  if ("on" in v && !v.on) return "Memory off: steps go to the log alone";
  const behind = subs.filter((s) => fulls(v.log[s]) > held(v.chat[s]));
  if (subs.every((s) => v.log[s].length === 0)) return "Nothing written";
  if (behind.length) return `${behind.join(" and ")}: step on disk, memory behind`;
  return v.failed ? "Every step in memory, a failure said" : "Every step in memory";
}

const STEPS = {
  BuildEnd: "a summary comes back",
  BuildFail: "saving the summary fails (a full disk)",
  MemFail: "writing its chat fails (a full disk)",
  Reuse: "its log is deleted, a new subagent takes the name",
  BuildIO: "the summary is saved to disk",
  BuildSc: "takes the chat's lock to save it",
  CloseAm: "waits for that memory's own lock",
  CloseIdle: "closes idle memories",
  CloseSc: "closes one, or skips it",
  Crash: "opt-chat crashes or restarts, memory on or off",
  FeedStep: "feeds its memory",
  Lock: "finds its entry",
  LockAm: "takes its own lock",
  LockSc: "takes its chat's lock",
  LogIO: "its chat is written to disk",
  OpenSet: "its memory opens",
  PumpNext: "the pump goes to the next chat",
  PumpSc: "the pump takes a chat's lock",
  StartPump: "a compaction ends: the pump runs",
  StartWrite: "a subagent starts a step",
  StartZoom: "Opt zooms",
  TornWrite: "the write is cut short",
  View: "Opt reads the view",
  WriteStep: "the step is written to its log",
  ZoomAgain: "Opt may zoom again",
};
export function step(action, before, after) {
  return STEPS[action] ?? action;
}

// Why a step waits on another subagent's disk: who holds the lock between.
export function cause(v, facts) {
  for (const a of Object.keys(v.pc).map((x) => JSON.parse(x))) {
    if (a[0] !== "w" && a[0] !== "z") continue;
    const hs = holders(v, a, facts);
    if (!hs.some((h) => inIO(v, h) && subj(v, h) !== subj(v, a))) continue;
    if (hs.some((h) => h[0] === "p")) return "the pump holds the subagents' map while it waits for a chat";
    if (hs.some((h) => v.pc[k(h)] === "cisc") || v.pc[k(a)] === "cisc") return "closing idle memories holds the map while it waits for a chat";
    return "another lock between";
  }
  return null;
}
