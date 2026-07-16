# CLIProxyAPI Local Patch Queue

Base upstream: `v7.2.80`

Apply from a clean upstream source tree, in this order:

```powershell
git apply E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch
git apply E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

This queue intentionally preserves only local source customizations. It does not include `config.yaml`, auth directories, auth JSON files, runtime `data`, caches, logs, or build output.

## Patch Order

1. `001-backend-local-customizations.patch`
   - 429 cooldown rule: consecutive 1 to 10 do not cool down, 11 starts cooldown.
   - A successful request clears the consecutive 429 count.
   - A non-429 result before cooldown clears the pending 429 count.
   - A non-429 failure must not clear an already active cooldown window.
   - 403 invalid workspace status: `无效认证`, reason `账号不是当前 workspace 成员`.
   - 401 invalid token status: `401认证失效`.
   - Remove endpoints and target directories derived from the current resolved `auth-dir`.
   - OpenAI/Codex OAuth JSON usage accounting and management APIs.
   - Auth file responses include zero-value `openai_usage` for eligible OpenAI/Codex OAuth JSON files even before any usage is recorded, so the panel can show the usage fields immediately.
   - Usage manager restart/unregister behavior needed by the OpenAI usage plugin.

2. `002-management-panel-local-ui.patch`
   - Management panel local UI changes.
   - Auth files page size 100.
   - Quota page size 100.
   - Buttons `403移除（N）` and `401认证失效（N）`.
   - OpenAI/Codex JSON card fields: `估算费用`, `输入 Token`, `缓存输入 Token`, `输出 Token`, `请求次数`, `Usage 缺失`, `计价缺失`.
   - OpenAI/Codex `.json` cards render the zero-value usage block even when the backend field is missing or no usage has been recorded.
   - Mojibake-sensitive status checks for invalid auth labels.

## Validation After Applying

Use the project-local module cache when available:

```powershell
$env:GOFLAGS='-buildvcs=false'
$env:GOTOOLCHAIN='local'
$env:GOMODCACHE='E:\CLIProxyAPI\.gomodcache'
$env:GOPROXY='off'
go test -count=1 ./...
go build -ldflags "-X main.Version=v7.2.80 -X main.Commit=unknown -X main.BuildDate=<UTC_BUILD_DATE>" -o E:\CLIProxyAPI\build-output\cli-proxy-api.exe ./cmd/server
```

Do not overwrite `E:\CLIProxyAPI\cli-proxy-api.exe` during validation. The build output remains `E:\CLIProxyAPI\build-output\cli-proxy-api.exe`.
