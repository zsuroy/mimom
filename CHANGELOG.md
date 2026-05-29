# Changelog

## [0.1.3] - 2026-05-29

### Fixed

- Responses API 转换全面重写，修复 Codex CLI 兼容性问题
  - 工具格式：Responses API 扁平格式 → Chat Completions 包装格式（`function` 字段）
  - 过滤非 function 工具类型（`custom`、`tool_search`）避免 MiMo 400 错误
  - `developer` 角色映射为 `system`（Codex 使用 developer，MiMo 不支持）
  - 数组内容提取文本，跳过 `thinking` blocks 避免 MiMo 拒绝
  - assistant 消息 `tool_calls` 转换为 Chat Completions 格式
  - 带 `tool_calls` 的 assistant 消息补充 `content: ""`（MiMo 要求字段存在）
  - 流式响应累积 content 文本写入 `output_item.done`，处理 `finish_reason: "stop"`
  - 移除多余 SSE 事件（`content_part.added`、`function_call_arguments.done`）
  - 添加 `data: [DONE]` 终止符
- 非流式 Responses API：支持 `tool_calls` 输出项，条件输出 message 项

## [0.1.2] - 2026-05-29

### Added

- Dashboard UI 全面改版：深色主题、Chart.js 实时图表（请求/秒折线图、延迟柱状图）、GitHub 链接
- 统计指标增强：平均/最大延迟、RPS、按秒聚合时序数据（120 点滑动窗口）
- 集成测试：`-tags integration` 打真实后端验证代理行为（模型路由、reasoning 缓存回填、流式转发等）
- Anthropic 路径感知路由：`/v1/messages` 优先匹配 Anthropic 后端，支持同名模型跨后端配置
- `LookupModelByType()` 按后端类型查找模型
- `FindAnthropicBackend()` 查找 Anthropic 后端
- Dockerfile 多阶段构建

### Fixed

- Claude 客户端请求 `/v1/messages` 时因模型名未匹配而 404 的问题

### Changed

- `maxRecentLogs` 从 50 提升到 100
- 轮询间隔从 5 秒缩短到 3 秒
- `config.yaml` 恢复为模板（去除真实 API Key）

## [0.1.0] - 2026-05-28

### Added

- OpenAI 兼容的 `/v1/*` 全量透传代理
- Anthropic Claude API 支持（`/v1/messages`，`thinking` blocks 缓存与回填）
- OpenAI Responses API 支持（`/v1/responses`，Codex CLI 兼容，自动转换为 Chat Completions）
- `reasoning_content` 字段自动缓存与回填（支持流式和非流式）
- 多后端配置：一个 `base_url` 下挂多个模型，模型名映射
- 请求日志：方法、路径、状态码、耗时
- 启动 banner：展示已加载的后端和模型
- CLI 参数：`-config`、`-version`
- 缓存 TTL（3 小时）+ LRU 淘汰 + 64MB 内存上限
- 可选代理鉴权（`server.api_key`）
- 配置文件 `base_url` 自动补全 `/v1`
- Docker 和 systemd 部署示例
