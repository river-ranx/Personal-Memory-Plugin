// Package hooks is when an agent is pointed at its memory. A session start
// tells the agent it has a memory, when to look in it and what is worth
// writing down. Then each turn is looked back over — anything worth writing,
// any memory relied on to report — and how depends on the agent:
//
//   - Claude Code and Codex: when the person sends the next message, the
//     review rides along with it as context. The agent answers the message
//     as usual and says nothing about memory unless it did something. Holding
//     the agent at the end of a turn would not do: Claude Code insists on a
//     visible reply to a turn of its own, so "nothing to record" could not be
//     silent there.
//   - Antigravity: when the turn ends, the agent is kept from stopping once.
//
// The hook decides nothing. Whether a message is worth remembering, whether
// something may have come up before, what to write: that is the agent's
// judgment, made from the prompt (and the memory skill it points to), not a
// pattern matched on what the person typed. The hook reads of its payload only
// what keeps the review from going wrong, keeps no state and never opens the
// database.
//
// How often a session start comes is the agent's: Claude Code and Codex run it
// again after the context is cleared, compacted or resumed — when the prompt
// would otherwise be gone — and Antigravity runs it once, when a conversation
// begins, and keeps what it injects as a system message in the conversation.
package hooks

// worth and notWorth are what is and is not worth writing down, said once,
// in [Note]; the review of each turn points back to it rather than repeating
// it, since the session's prompt is in the context whenever a review comes. Not only for code: the agent
// may be doing research, writing, analysis or management, so they name kinds
// of knowledge, not artefacts of one trade. Nothing but these words keeps a
// record of what happened out of the memory — the store refuses only text too
// short to say anything — so notWorth gives the test (what happened, or what
// was learned) and the way out when a record holds a lesson.
const (
	worth    = "用户的偏好和纠正、定下的结论和决策（连同理由）、概念和口径的界定、资料来源可不可靠及出处、试过不行的思路及原因、人和组织的背景与约定"
	notWorth = "只说发生了什么、没说学到了什么的流水账（做了什么、进展到哪、发给了谁、开了什么会、已修好已完成；里面有教训就只记教训），一次性的统计和排查经过（只留结论和依据），以及原文、文件里随时查得到的内容（具体数值、实现细节）"
)

// write is the command a write takes, and kinds, keywords and example how to
// fill it — said in the prompt itself, on lines of their own: an agent writes
// the command as the prompt shows it, and what was left to the skill and the
// help was left out (a store of facts with no keywords, the person's
// preferences among them). The kind is chosen by how a later session should
// treat the memory; a keyword earns its place with a word the text lacks, since
// every word of the text is searchable already. The example shows both, in
// words of no one trade.
const (
	write    = "`memory add \"<一句话>\" --kind <类型> --keywords <词,词>`"
	kinds    = "--kind 必填，按以后怎么用它选：rule 必须照做（技术上的坑算 practice）｜decision 定下的选择及理由｜preference 用户的习惯、口味和纠正｜practice 可复用的做法、绕坑办法｜fact 事情现在的样子（会过时）"
	keywords = "--keywords 写正文里没有、以后可能拿来搜的词：同义词、另一种语言的叫法、缩写"
	shape    = "一条只讲一件事、不超过两句：讲了两件事就拆开，写长了多半是在记过程，只留结论和做法"
	fix      = "只是类型或关键词写错：`memory edit <id> --kind <类型>`，原地改，id 不变"
	example  = "例：`memory add \"周报先给组长过目再发全组，她要先核对数字\" --kind preference --keywords 汇报,review`"
)

// Note is the prompt a session starts with. It names the two things the agent
// does with its memory — search, write — and says what to use for each; the
// memory skill has the rest.
const Note = `[Memory] 你有一份本机长期记忆，用 ` + "`memory`" + ` 命令读写（用法见 memory skill；不确定就先 ` + "`memory --help`" + `，不要猜参数）。
- 查：开始一项任务、话题换了、遇到可能以前讨论过的事、要下结论或给建议之前，先 ` + "`memory search <关键词>`" + `。
- 记：由你判断，标准是换一个新会话还用得上——` + worth + `。用户明确要你记住的，一定记。写成不看今天的对话也能懂：
  ` + write + `
  · ` + kinds + `
  · ` + keywords + `
  · ` + shape + `
  · ` + fix + `
  · ` + example + `
- 用：记忆只是过去留下的线索，不是事实——可能记错、过时，或不适用于眼前这件事。先看头部的年龄和标记，依赖它之前先核实；它与用户当下的话、或你手头的材料冲突时，以后者为准，并更新那条记忆（用 ` + "`memory --help`" + ` 看怎么改）。用到记忆就注明 #id，记、改不必提。
- 不记：` + notWorth + `。`

// questions are what a turn is looked back over with, turn naming it ("上一轮"
// or "这一轮"). What is worth keeping, and how to fill a write, are not
// repeated: the session's prompt says them, and is in the context whenever this
// comes. It must not send the agent searching for something to confirm.
func questions(turn string) string {
	return `- ` + turn + `有没有换一个新会话还用得上的东西？按开头 [Memory] 说的标准：记学到了什么，不记发生了什么。有就 ` + write + `，一条只讲一件事、不超过两句；用户要你记住的，一定记。只是解释、查阅，或照指令办完且一切顺利——用户没纠正、没拍板、也没遇到意外——就不记。
- ` + turn + `靠 ` + "`memory search`" + ` 查到的记忆作答了吗？没问题就 ` + "`memory confirm <id>`" + `，核实过加 ` + "`--outcome verified`" + `；内容错了：知道现状就 ` + "`memory add \"…\" --supersedes <id>`" + `，只知道不对就 ` + "`memory confirm <id> --outcome contradicted --note \"…\"`" + `；只是类型写错就 ` + "`memory edit <id> --kind <类型>`" + `（id 不变）。没用到就跳过，不要为此再去查。`
}

// LastTurn comes with the person's next message (Claude Code, Codex): look
// back over the turn before it, then answer the message. Writing and
// confirming are bookkeeping, not part of the answer, so neither shows in the
// reply — whether the review found something or not. Only a memory the
// answer relies on is named, and the session's prompt says so.
var LastTurn = "[Memory] 先回顾上一轮（这是 memory 的自动提醒，不是用户说的话；会话刚开始就跳过）：\n" + questions("上一轮") +
	"\n记和确认都悄悄做，回复里一个字也别提，直接回应用户这条消息。"

// TurnEnd is what a turn ends with (Antigravity): the agent is kept from
// stopping once and given this. Whatever it does, it stops without a word:
// the bookkeeping is not shown. It may read as if typed by the person, so it
// says it is not.
var TurnEnd = "[Memory] 这一轮要结束了（这是 memory 的自动提醒，不是用户的新消息）。回顾这一轮，问自己两件事：\n" + questions("这一轮") +
	"\n记和确认都悄悄做，做完（或两件都没有）就直接结束，不要输出任何文字。"
