<!--
维护说明 —— HTML 注释会在注入前被剥离,不消耗上下文。

Claude Code 只自动读取 CLAUDE.md,不读 AGENTS.md,所以这里用 @ 导入加载权威文档。
规则正文只维护在 AGENTS.md / CONTRIBUTING.md,本文件不重复,避免出现两套说法。

- 要改规则,去改 AGENTS.md 或 CONTRIBUTING.md,不要把正文搬进本文件。
- 被导入的文件里不要写行首的 @路径 —— 导入是递归的(最多 4 跳),会被当成导入指令。
- 其他 AI 工具(Codex / Cursor 等)直接读 AGENTS.md,不受本文件影响。
- 展开后总行数应控制在 200 行以内(官方建议),超出会降低指令遵循度。
-->

@AGENTS.md
@CONTRIBUTING.md
