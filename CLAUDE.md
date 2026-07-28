# CLAUDE.md

Claude Code 只会自动读取本文件,**不读 `AGENTS.md`**。所以这里用 `@` 导入把两份权威文档
完整加载进上下文 —— 内容仍然只存在于被导入的文件里,本文件不重复,不产生第二套说法。

其他 AI 工具(Codex / Cursor 等)直接读 `AGENTS.md`,不受这里影响。

> 改动提示:不要把规则正文搬到本文件。要改规则,去改 `AGENTS.md` 或 `CONTRIBUTING.md`。
> 也不要在被导入的两份文件里写行首的 `@路径` —— 导入是递归的(最多 4 跳),会被当成导入指令。

@AGENTS.md
@CONTRIBUTING.md
