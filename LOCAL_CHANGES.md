# 本地定制变更记录

本文档用于记录本项目在同步 CLIProxyAPI 上游源码或更新管理面板之后，必须恢复和保留的本地定制内容。

## 1. HTTP 429 冷却行为

本地规则：
- 同一个认证文件/账号连续出现第 1 至第 10 次 HTTP 429 时，不进入冷却。
- 从连续第 11 次 HTTP 429 开始，才触发冷却。
- 任意一次成功请求必须清空连续 429 计数。
- 在尚未进入 429 冷却时，任何非 429 结果必须清空对应 auth/model 的待累计 429 计数。
- 已经处于有效 429 冷却窗口时，并发返回的非 429 失败不能提前解除冷却；成功请求仍按成功逻辑清理。

当前重点文件：
- `sdk/cliproxy/auth/conductor.go`

上游更新后必须确认该逻辑仍保留。

## 2. 管理面板分页定制

本地规则：
- 认证文件页面每页默认/最大显示 100 个文件。
- 配额管理页面每页显示 100 个文件。
- 浏览器旧状态中保存的 30/其他分页值不能覆盖本地默认 100。

源码层位置：
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\pages\AuthFilesPage.tsx`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\features\authFiles\constants.ts`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\features\authFiles\uiState.ts`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\components\quota\QuotaSection.tsx`

构建产物检查标记：
- `LO=100,RO=100`
- `regularPageSize:100`
- `compactPageSize:100`

## 3. 管理面板更新流程

本地规则：
- 管理面板更新必须先从官方 CPAMC 源码更新/构建，再复制产物到 `E:\CLIProxyAPI\static\management.html`。
- 禁止把压缩后的 `static\management.html` 当作长期维护源码手工修改。
- 当前官方面板源码目录固定使用：`E:\CLIProxyAPI\.codex-sync\cpamc-panel-source`
- 官方源码仓库：`https://github.com/router-for-me/Cli-Proxy-API-Management-Center`
- 更新前必须备份当前面板到 `E:\CLIProxyAPI\static`。
- 构建完成后复制生成的单文件面板覆盖 `E:\CLIProxyAPI\static\management.html`。
- 更新后必须确认 `config.yaml` 保持：

```yaml
remote-management:
  disable-auto-update-panel: true
```

## 4. 403/401 认证移除按钮

本地规则：
- 认证文件页面顶部操作区中，上传按钮必须紧跟在刷新按钮后面。
- `inactive-workspace` 状态仍显示为 `无效认证`，按钮文案为 `403移除（N）`。
- `401认证失效` 状态仍显示为 `401认证失效`，按钮文案为 `401移除（N）`。
- 403 和 401 移除流程必须相互独立。
- 401 认证失效文件移动目录保持：`C:\Users\admin\.cli-proxy-api\removed\401-invalid\`。

源码层位置：
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\pages\AuthFilesPage.tsx`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\features\authFiles\hooks\useAuthFilesData.ts`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\services\api\authFiles.ts`
- `internal/api/handlers/management/auth_files.go`

构建产物检查标记：
- `403移除`
- `401移除`
- `无效认证`
- `401认证失效`

## 5. OpenAI 用量/计费 UI 与全额度

本地规则：
- OpenAI 用量/计费块属于本地定制，必须在官方 CPAMC 源码层保留。
- 禁止长期直接手工修改压缩后的 `static\management.html` 来维护该 UI。
- 认证文件页面必须保持官方卡片网格布局；宽屏应约 3 张卡片一行。
- OpenAI 用量/计费块必须在卡片内部可读，不能把卡片撑成整行。
- 不允许 8 个字段强行挤在一行。
- 不允许 `估算费用`、`输入 Token` 等标签被拆成碎字。
- 不允许金额或 Token 数字被拆成不可读的多段。
- 窄卡片时可以自动换行成可读的纵向/双列布局。
- `全额度` 属于本地定制，显示在认证文件卡片的 OpenAI 用量/计费块中。
- `全额度` 数据来源是 Codex quota 接口原始 `used_percent` 与 OpenAI usage 等价 API 估算费用的增量计算。
- 后端采样只允许从 Codex 主额度 `rate_limit` / `rateLimit` 读取，不允许采样 `code_review_rate_limit` / `codeReviewRateLimit` 或 `additional_rate_limits` / `additionalRateLimits`。
- 后端采样必须同时检查主额度里的 `primary_window` / `primaryWindow` 和 `secondary_window` / `secondaryWindow`，并按 `limit_window_seconds` / `limitWindowSeconds` 识别窗口。
- 后端采样窗口识别固定为：`604800` 秒是 `weekly`；8 到 31 天秒数范围是 `monthly`；`18000` 秒是 5 小时窗口且必须忽略。
- 后端采样必须优先选择 `weekly`，没有 `weekly` 时才选择 `monthly`；如果已选中的窗口没有有效 `used_percent` / `usedPercent`，必须直接不采样，不能回退到另一个窗口。
- 只有主额度两个窗口都缺少窗口秒数字段时，才允许旧格式兼容：仅 fallback 到 `secondary_window` / `secondaryWindow`，并按 `weekly` 处理；不能 fallback 到 `primary_window`。
- 第一次额度刷新成功只建立 baseline；第二次及以后只有窗口一致、`used_percent` 增加至少 1%、估算费用增加且 `Usage 缺失`/`计价缺失` 未新增时才显示估算金额。
- 没有有效样本时，前端显示 `全额度 采样中`。
- 管理面板产物必须从 CPAMC `dist\index.html` 复制到 `static\management.html`，不要手工修改压缩后的 `static\management.html`。

必须保留字段：
- `估算费用`
- `输入 Token`
- `缓存输入 Token`
- `输出 Token`
- `推理 Token`
- `请求次数`
- `Usage 缺失`
- `计价缺失`
- `全额度`

源码层位置：
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\features\authFiles\components\AuthFileCard.tsx`
- `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source\src\pages\AuthFilesPage.module.scss`
- `internal\openaiusage\store.go`
- `internal\api\handlers\management\api_tools.go`
- `internal\api\handlers\management\api_tools_test.go`

安全规则：
- `internal\api\handlers\management\api_tools.go` 禁止硬编码 Antigravity Google OAuth `client_id` / `client_secret`。
- Antigravity OAuth token 刷新时，优先读取认证 metadata 中的 `client_id` / `client_secret`、`clientId` / `clientSecret`、`oauth_client_id` / `oauth_client_secret`。
- 认证 metadata 缺失时，读取环境变量 `ANTIGRAVITY_OAUTH_CLIENT_ID` 和 `ANTIGRAVITY_OAUTH_CLIENT_SECRET`。
- metadata 和环境变量都缺失时，刷新必须返回明确错误，不能回退到源码中的真实 secret。

补充规则：
- `估算费用` 只在前端显示保留 2 位小数。
- 其他 `Token`、请求次数、缺失计数仍保持整数显示。
- 当前认证文件卡片内的 OpenAI 用量区必须使用横向 `标签: 数值` 紧凑流式布局，多个字段自动换行，但单个字段不能回到上下堆叠的大块结构。
- 该区域仍需保留 `min-width: 0`、`max-width: 100%`、`overflow: hidden`、`overflow-wrap`、`word-break` 等防撑宽约束。
- 后续更新面板时，必须优先检查紧凑布局和两位小数格式化逻辑仍保留。
- 后续更新后端时，必须优先检查 `quotaSampleWindowFromUsageResponse` 仍按主 `rate_limit` 的 weekly/monthly 规则采样，且 weekly 缺少 `used_percent` 时不 fallback 到 monthly。

验收标准：
- 认证文件页仍是官方 `fileGrid` 卡片网格。
- 宽屏约 3 卡一行。
- OpenAI 用量/计费标签和数字可读，且 `估算费用` 显示为两位小数。
- 不出现 `repeat(auto-fit,minmax(0,1fr))` 这种把 8 项压碎的计费区实现。
- `全额度` 有有效样本时显示两位小数美元；没有有效样本时显示 `采样中`。
- `api_tools_test.go` 保留主额度窗口采样测试：primary/secondary 顺序异常、camelCase、旧格式 secondary fallback、忽略 code review、忽略 additional、忽略 5 小时窗口、weekly 缺少 `used_percent` 不回退 monthly。

## 6. 插件中心

本地规则：
- 保留官方插件相关入口和代码。

构建产物检查标记：
- `plugin_store`
- `插件商店`
- `supportsPlugin`
- `/plugins`

## 7. 固定后端编译输出目录

本地规则：
- 后端正式编译产物固定输出到：`E:\CLIProxyAPI\build-output\cli-proxy-api.exe`
- 除非明确要求，不要覆盖项目根目录中正在运行的 `E:\CLIProxyAPI\cli-proxy-api.exe`。
- 管理面板更新任务不需要重新编译后端 exe。

## 8. sub2api Agent Identity 认证导入与请求认证

本地规则：
- CPA 支持导入 sub2api 导出的顶层 `accounts[]` Agent Identity JSON。
- 支持固定认证模式 `auth_mode = agentIdentity`。
- 导入时只处理 `account.type = oauth`、`credentials.auth_mode = agentIdentity`、`agent_private_key` 非空、`agent_runtime_id` 非空的账号。
- `task_id` 可以为空；`workspace_id` 可以为空，但存在时必须保存。
- 同邮箱不同 `workspace_id` 必须生成不同认证文件，不能覆盖。
- 同邮箱同 `workspace_id` 也不能覆盖已有文件，除非 CPA 原有导入逻辑明确支持更新。
- Agent Identity 请求必须使用 `Authorization: AgentAssertion <base64url-json>`，不能使用 `Bearer <access_token>`。
- Agent Identity 模式不能走普通 OAuth refresh，不能调用 `oauth2.googleapis.com/token`。
- `task_id` 缺失或请求返回明确 `invalid_task_id` 时，允许按 sub2api 规则注册 task，并把新 `task_id` 写回当前认证文件；单次请求最多重试一次。
- Agent Identity 成功请求必须进入现有 OpenAI usage / 全额度统计链路，且按 `auth_index` 归属；同邮箱不同 workspace 分开统计。
- OAuth JSON、API Key、Agent Identity 三类认证不能互相影响。

安全规则：
- `agent_private_key`、`id_token`、`task_id`、`access_token`、`refresh_token`、API Key 不允许出现在日志、面板、错误返回中。
- 面板/管理 API 展示 Agent Identity 时只能展示认证模式、邮箱、Workspace、Plan，以及截断后的 `agent_runtime_id`。
- `agent_runtime_id` 最多展示前 6 位 + 后 4 位。
- `task_id` 不显示完整值。

协议来源：
- `E:\CLIProxyAPI\.codex-sync\sub2api-src\backend\internal\service\openai_agent_identity.go`
- `E:\CLIProxyAPI\.codex-sync\sub2api-src\backend\internal\handler\admin\account_codex_import.go`
- `E:\CLIProxyAPI\.codex-sync\sub2api-src\backend\internal\handler\admin\account_codex_agent_identity_import_test.go`
- `E:\CLIProxyAPI\.codex-sync\sub2api-src\backend\internal\service\openai_agent_identity_test.go`
- `E:\CLIProxyAPI\.codex-sync\sub2api-src\backend\internal\service\openai_agent_identity_compat_test.go`

当前重点文件：
- `internal/api/handlers/management/auth_files.go`
- `internal/runtime/executor/codex_executor.go`
- `internal/runtime/executor/codex_openai_images.go`
- `internal/runtime/executor/codex_websockets_executor.go`
- `internal/runtime/executor/helps/agent_identity.go`
- `sdk/cliproxy/auth/classification.go`
- `sdk/cliproxy/auth/conductor.go`

## 9. 更新后检查清单

同步上游源码或面板后：
1. 确认 429 第 11 次冷却逻辑仍保留。
2. 确认认证文件页面和配额管理页面仍为每页 100。
3. 确认 `config.yaml` 中 `remote-management.disable-auto-update-panel: true` 未被改动。
4. 确认 `403移除（N）` / `401移除（N）` 仍存在。
5. 确认 OpenAI 用量/计费 UI 仍在认证文件卡片内，`估算费用` 仍保留两位小数，且宽屏卡片网格不被撑坏。
6. 确认插件中心标记仍存在。
7. 确认 Agent Identity 导入、AgentAssertion 请求、task 注册恢复、usage / 全额度统计仍保留。
8. 确认 `agent_private_key`、`id_token`、`task_id`、`access_token`、`refresh_token`、API Key 未泄露到日志、面板、错误返回。
9. 如果生成后端 exe，只能输出到 `E:\CLIProxyAPI\build-output\cli-proxy-api.exe`。

