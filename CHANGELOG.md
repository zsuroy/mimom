# Changelog

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
- 缓存 TTL（30 分钟）+ LRU 淘汰 + 64MB 内存上限
- 可选代理鉴权（`server.api_key`）
- 配置文件 `base_url` 自动补全 `/v1`
- Docker 和 systemd 部署示例
