# memory — 本机的长期记忆

给 AI Agent 用的长期记忆，存在这台电脑上。Claude Code、Codex、Antigravity 共用一份：
一个 SQLite 文件、一个命令行 `memory`、一个教 Agent 怎么用的 skill、两个 Hook。
写代码、做研究、写作、分析、管理都适用。

```
Hook ──┬─ 会话开始：告诉 Agent 它有记忆、什么时候查、什么值得记
       └─ 每一轮：提醒它回顾——有没有要记的、有没有要确认的

Agent ── memory search / add / confirm ──▶ ~/.memory/memory.db
```

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/river-ranx/Personal-Memory-Plugin/main/install.sh | sh
```

支持 macOS 和 Linux（arm64、amd64），不需要 Go，也不需要装 SQLite。脚本会：

1. 下载对应平台的 `memory`，用发布里的 `SHA256SUMS` 校验，放到 `~/.local/bin/memory`；
2. 给本机找得到的 Agent（有配置目录或命令的）装好 skill 和两个 Hook；
3. 记忆库 `~/.memory/memory.db` 在第一次使用时自动创建。

可选的环境变量：`MEMORY_AGENTS="claude codex"` 只装这几家，`MEMORY_VERSION=v0.1.0` 装指定版本，
`MEMORY_BIN_DIR` 改放命令的目录。再跑一次就是升级，记忆不受影响。Codex 装完要在 Codex 里跑一次 `/hooks` 信任新 Hook。

卸载：`memory uninstall claude codex antigravity && rm ~/.local/bin/memory`（记忆库不删，要删就删 `~/.memory`）。

| Agent | Skill | Hook 配置 |
| --- | --- | --- |
| Claude Code | `~/.claude/skills/memory/` | `~/.claude/settings.json` 的 `hooks` |
| Codex | `$CODEX_HOME/skills/memory/` | `$CODEX_HOME/hooks.json`；装完在 Codex 里跑一次 `/hooks` 信任它们 |
| Antigravity | `~/.gemini/config/skills/memory/` | `~/.gemini/config/hooks.json` 的 `memory` 项 |

安装只动自己的条目，其余配置原样保留；第一次改某个配置文件前，原文件存为 `<文件>.memory-backup`。

## 用法

```sh
memory search 报销 流程 OA [--limit N]
memory add "<一句话>" [--kind preference|fact|rule|decision|practice] [--title T] [--keywords a,b,c] [--supersedes ID]
memory confirm <id…> [--outcome used|verified|contradicted] [--note N]
memory show <id> | list [--kind K] [--archived] [--limit N]
memory archive <id…> | restore <id…> | delete <id…>
memory --help | memory <命令> --help
```

- id 是自增数字，写 `12` 或 `#12`，删除后不复用。每个命令都接受 `--json` 和 `--project DIR`。
- 项目就是调用时的工作目录：同一目录、它的子目录和上级目录算同一个项目，排在前面；别的项目的照样查得到，
  并标出来自哪里。
- 数据库位置用 `MEMORY_HOME` 改，默认 `~/.memory/memory.db`。

## 设计

- **判断交给 Agent。** 查不查、记什么，由 Agent 读了提示词和 skill 之后自己决定。Hook 不看用户说了什么，
  写入只拦两种：太短（不到 3 个词）和重复。
- **记学到了什么，不记发生了什么。** 偏好和纠正、结论和决策（连同理由）、概念和口径、资料来源及出处、
  试过不行的思路、人和组织的背景——这些值得记；进展、已发给谁、已合并之类的流水账不记，里面有教训就只记教训。
  这条标准在提示词、skill、`memory add --help` 里是同一套说法，靠提示而不是靠程序拦。
- **记忆是线索，不是事实。** 结果的头部先说明怎么对待正文：类型、多久以前写的、有没有核对过或被反驳过、
  是不是别的目录的。信任顺序：用户现在说的 > 眼前看到的 > 记忆；不一致时以前者为准，并修正那条记忆。
- **为 Agent 设计的命令行。** 输出是头部行加正文；参数打错直接报错并列出能用的；每个错误都带下一步
  （用法行、现成的 `--supersedes` 命令、换个说法再搜）；帮助分总览和单条命令两层；输出有上界。
- **库在本机。** 写进去的就是存下的，不拦、不脱敏凭证和联系方式。

## 写入

- 只有正文必填，`--kind` 默认 `fact`；一条最多 1000 字。标题折成一行、最多 60 字；关键词去重、最多 10 个，
  权重是正文的两倍。
- **重复**：和最接近的 5 条比词的 Jaccard 相似度，≥ 0.4 就不写，给出已有的那条和取代它的现成命令。
- **取代**（`--supersedes ID`）：没写的类型、标题、关键词继承旧的。只有新记忆记着它取代了谁；
  新的在用时旧的不返回，新的被归档或删除，旧的立刻重新可用。
- **确认**（`confirm`）：同一条记忆、同一种结果，一天只算一次。自上次被反驳以来（不论隔多久）在三个不同的日子
  核对过，才标为 `verified`；被反驳一次就撤销，之后重新累计。

## 搜索

- 分词：中文按两字窗口（`报销` 能在「这次报销怎么走」里命中）；英文按词，两个字母的缩写也算（`ci`、`oa`），
  单个字母和 `to`、`the` 这类虚词不算；中英文紧挨着写时在边界切开（`用CI跑` 里的 `ci`）。
- 打分：FTS5 的 bm25（标题 3、关键词 2、正文 1），再乘同项目、使用次数、核对、反驳几个因子；
  衰减从最近一次使用算起，90 天减半，最低三成。
- 被取代的、已归档的不返回。活跃记忆 ≥ 30 条时，只靠一个无关词沾边的结果（弱于最佳的 10%）会被裁掉。
- 「被搜到」只用来防止衰减（每条每天最多记一次），不算使用；使用次数只算 Agent 用 `confirm` 报告的。
  所以一条记忆不会因为常被搜出来就自己越排越前。

## 存储

一个 SQLite 文件，三张表，打开时 `CREATE … IF NOT EXISTS`，没有迁移：

- `memories`：记忆本身；`supersedes` 指向它取代的那条，写入后不再改。
- `memory_events`：每条记忆怎么被用的——被搜到、用过、核对过、被反驳。次数、最近一次使用、衰减都从这里算；
  180 天前的「被搜到」在写入时顺带清理（每条保留最近一次）。
- `memories_fts`：全文索引，存分词后的结果。改了分词规则，已有库的索引不会自动重建。

## Hook

**会话开始**注入一段固定的提示词（`hooks.Note`），什么值得记、什么不记都写在这里。之后**每一轮都会被回顾一次**：
这一轮有没有值得记的、有没有靠记忆做了事要 `confirm` 的。回顾不重复判断标准，只指回开头那段。

| | 什么时候回顾 | 没东西可记时 |
|---|---|---|
| Claude Code、Codex | 用户发下一条消息时，提醒（`hooks.LastTurn`）随消息一起进上下文，Agent 先回顾上一轮再回应 | 一个字不提，直接回应用户；不多跑一轮 |
| Antigravity | 一轮结束时拦住 Agent 一次，给它提醒（`hooks.TurnEnd`） | 直接结束，不输出文字 |

Claude Code 和 Codex 不在一轮结束时拦：被拦下的那一轮如果空着结束，Claude Code 会追加一句「请给出用户可见的回复」
再跑一轮（实测，程序里有这段原文），所以「没东西可记就不出声」只能靠把回顾挪到下一条消息里做到。
代价是会话的最后一轮不会被回顾——会话开始的提示词本来就要求边做边记。

回顾在这些情况下不发：

- 启动 Agent 时设了 `MEMORY_REVIEW=off`。用 `claude -p` 跑脚本、CI 时设上：钩子收到的数据和环境变量分辨不出
  「脚本」和「有人在用的 SDK 应用」，只能由调用方说明；
- 计划模式，Agent 跑不了命令；
- Codex 子代理的任务（带 `agent_id`），那不是用户的消息；
- Antigravity：这次停止就是提醒引出的那一次（否则会无限循环）、不是正常结束（出错、用户取消、到了调用上限），
  或收到的数据读不懂（宁可漏一次，也不冒循环的险）。

Hook 不碰数据库、不留状态，任何情况都以 0 退出，不让 Agent 失败。

|  | Claude Code | Codex | Antigravity |
| --- | --- | --- | --- |
| 会话开始何时触发 | 启动、恢复、`/clear`、压缩后 | 同左，另有 fork | 只在新对话开始时一次 |
| 会话开始的输出 | `hookSpecificOutput.additionalContext` | 同左 | `{"injectSteps":[{"systemMessage":{"systemMessage":"…"}}]}` |
| 回顾挂在 | `UserPromptSubmit` | `UserPromptSubmit` | `Stop` |
| 回顾的输出 | `hookSpecificOutput.additionalContext` | 同左 | `{"decision":"continue","reason":"…"}`；不提醒时 `{"decision":"stop"}` |
| 信任 | 不需要 | 新 Hook 要先在 `/hooks` 里信任 | 不需要 |

格式核对自 Codex 0.170 程序内嵌的 Hook JSON Schema 和 Antigravity（`agy` 1.5.12）内嵌的 Hook 文档。

## 开发

```sh
make build                      # 编到 bin/memory
make test                       # go vet + go test
make install                    # 从源码装：编到 ~/.local/bin/memory，给三家装好 skill 和 Hook
make dist                       # 交叉编译 dist/memory-<系统>-<架构> 和 SHA256SUMS
```

发布：`make dist`，再把 `dist/` 里的文件传到一个 GitHub Release，`install.sh` 默认装最新的那个。

