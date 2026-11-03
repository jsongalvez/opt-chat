# What this fork adds to delneg/opt-chat

One row per feature the fork carries over upstream (delneg master def9267,
Nov 3 2026). Keep this table current: a feature that lands upstream, or that
we drop, leaves it in the same commit.

| Feature | Flag / files | In use? | Upstream? | Size |
|---|---|---|---|---|
| Subagent memory: a summary tree per subagent, search and recall reach it | `-agent-memory`, `-agent-jobs`; agentmem*.go, formal/agentmem | yes | PR #6, ready | large |
| Owed list: what the user asked for, closed on their word | `-owed`; owed.go | yes, every turn | no PR | 364 lines |
| Resume at the usage limit's reset, per caller | resume.go, formal/limitresume, .claude/skills/limit-resume | yes | merged except the wording fix in PR #2 | medium |
| Summaries say only what their input says; re-ask a compaction that copies from context | `-compact-copied`, prompt.md | yes | no PR | small |
| Rebuild only bad summary lines | `-redo`, `-eval-compact` | one-off (memrot) | no PR | small |
| Compactions under nice | `-compact-nice` | yes | no PR | tiny |
| Experimental relay: cache marks in transit | `-experimental-relay`; relay.go | **off since Nov 3** | no | 736 lines |
| OpenCode / MCP chat import | `-import-only`; import.go, tools/opencode-to-optchat | **unused since DeepSeek was dropped** | no | ~340 lines |
| MCP tools/list with sorted keys | mcp.go | yes (cache-stable tools) | no PR | tiny |
| Log a Cmd+Enter message to a subagent before the interrupt | subs.go | yes | no PR | tiny |
| Fork CI and upstream-branch CI | .github/workflows/ci.yml, tools/ci-upstream-branch.sh, tools/fork-rebuild-check.sh | yes | fork only | small |
| Cache-miss causes in the usage log, `-cache-ttl` | branch perf-bench (8d003c6); tools/perf | not yet live | separate PR, later | small |

Not on fork master yet: upstream def9267 (make your own subagent from the page).
