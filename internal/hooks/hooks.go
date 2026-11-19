// Package hooks is when an agent is pointed at its memory. There are two
// moments: a session starting, when the hook tells the agent it has a memory,
// when to look in it and what is worth writing down; and a turn ending, when it
// asks the agent to look back over the turn — anything worth writing, any
// memory it relied on to report — before it stops.
//
// The hook decides nothing. Whether a message is worth remembering, whether
// something may have come up before, what to write: that is the agent's
// judgment, made from the prompt (and the memory skill it points to), not a
// pattern matched on what the person typed. The hook reads of its payload only
// what keeps the turn-end reminder from going wrong — whether this stop is
// already the one the reminder asked for, whether the agent can act at all —
// keeps no state and never opens the database.
//
// How often a session start comes is the agent's: Claude Code and Codex run it
// again after the context is cleared, compacted or resumed — when the prompt
// would otherwise be gone — and Antigravity runs it once, when a conversation
// begins, and keeps what it injects as a system message in the conversation.
package hooks

// worth and notWorth are what is and is not worth writing down, said once
// for both prompts so they cannot drift apart. Not only for code: the agent
// may be doing research, writing, analysis or management, so they name kinds
// of knowledge, not artefacts of one trade. Nothing but these words keeps a
// record of what happened out of the memory — the store refuses only text too
// short to say anything — so notWorth gives the test (what happened, or what
// was learned) and the way out when a record holds a lesson.
const (
	worth    = "用户的偏好和纠正、定下的结论和决策（连同理由）、概念和口径的界定、资料来源可不可靠及出处、试过不行的思路及原因、人和组织的背景与约定"
	notWorth = "只说发生了什么、没说学到了什么的流水账（做了什么、进展到哪、发给了谁、开了什么会；里面有教训就只记教训），以及原文、文件里随时查得到的内容"
)

// Note is the prompt a session starts with. It names the two things the agent
// does with its memory — search, write — and says what to use for each; the
// memory skill has the rest.
const Note = `[Memory] 你有一份本机长期记忆，用 ` + "`memory`" + ` 命令读写（用法见 memory skill；不确定就先 ` + "`memory --help`" + `，不要猜参数）。
- 查：开始一项任务、话题换了、遇到可能以前讨论过的事、要下结论或给建议之前，先 ` + "`memory search <关键词>`" + `。
- 记：由你判断，标准是换一个新会话还用得上——` + worth + `。一条一句，写成不看今天的对话也能懂，用 ` + "`memory add \"…\"`" + `。用户明确要你记住的，一定记。
- 用：记忆只是过去留下的线索，不是事实——可能记错、过时，或不适用于眼前这件事。先看头部的年龄和标记，依赖它之前先核实；它与用户当下的话、或你手头的材料冲突时，以后者为准，并更新那条记忆（用 ` + "`memory --help`" + ` 看怎么改）。
- 不记：` + notWorth + `。`

// TurnEnd is what a turn ends with: the agent is kept from stopping once and
// given this. Most turns leave nothing to do, so the way out — say nothing,
// stop — is spelled out, and the reminder must not send the agent searching
// for something to confirm. It may arrive as if typed by the person (Codex
// continues the turn with it as the prompt), so it says it is not.
const TurnEnd = `[Memory] 这一轮要结束了（这是 memory 的自动提醒，不是用户的新消息）。回顾这一轮，问自己两件事：
- 有没有换一个新会话还用得上的东西？比如` + worth + `。有就 ` + "`memory add \"…\"`" + `，一条一句，写成不看今天的对话也能懂；用户要你记住的，一定记。不记：` + notWorth + `。
- 这一轮靠 ` + "`memory search`" + ` 查到的记忆作答或做事了吗？没发现问题就 ` + "`memory confirm <id>`" + `，对照原始来源核实过就加 ` + "`--outcome verified`" + `；发现它错了：知道现在是什么样就 ` + "`memory add \"…\" --supersedes <id>`" + `，只知道它不对就 ` + "`memory confirm <id> --outcome contradicted --note \"…\"`" + `。没用到就跳过，不要为此再去查。
两件都没有就直接结束，不要输出任何文字；做了就用一句话告诉用户记了或改了什么。`
