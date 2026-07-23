# CLIProxyAPI Local Patch Queue

Base upstream: `v7.2.95`

This queue stores the local source customizations that must be restored after an
official CLIProxyAPI source refresh. It intentionally excludes runtime data:
`config.yaml`, auth directories, auth JSON files, `data`, caches, logs, and build
output.

Apply from a clean upstream source tree, in this order:

```powershell
git apply E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch
git apply E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

The CPAMC management-panel source patch is applied separately inside the panel
source checkout, not the CLIProxyAPI backend checkout:

```powershell
git -C E:\CLIProxyAPI\.codex-sync\cpamc-panel-source apply E:\CLIProxyAPI\.codex-local-patches\003-cpamc-panel-source-local-ui.patch
```

On Windows trees checked out with `core.autocrlf=true`, use this form if Git
reports whitespace-only context conflicts:

```powershell
git apply --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch
git apply --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

For the CPAMC source patch, use this form if whitespace-only context conflicts
are reported:

```powershell
git -C E:\CLIProxyAPI\.codex-sync\cpamc-panel-source apply --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\003-cpamc-panel-source-local-ui.patch
```

## Patch Order

1. `001-backend-local-customizations.patch`
   - 429 cooldown rule: consecutive responses 1 through 10 do not cool down, the 11th starts cooldown.
   - Successful requests clear the consecutive 429 count.
   - Non-429 results before cooldown clear pending 429 counts.
   - Non-429 failures do not clear an already active cooldown window.
   - 403 invalid-workspace-member detection and removal remain separate from other 403 errors.
   - 401 invalid-token detection and removal remain separate from 403 invalid workspace handling.
   - Removal target directories are derived from the configured auth dir.
   - OpenAI usage accounting APIs are preserved.
   - OpenAI usage accounting now includes OpenAI/Codex OAuth JSON, OpenAI API Key, and openai-compatible API Key calls.
   - API Key account display remains redacted as `api-key:<auth_index prefix>`.
   - Pricing uses the embedded Sub2API / Wei-Shaw LiteLLM-compatible price table.
   - `全额度` estimates are preserved for Codex/OpenAI OAuth auth files.
   - `全额度` backend logic is in `internal/api/handlers/management/api_tools.go` and covered by `internal/api/handlers/management/api_tools_test.go`.
   - `全额度` sampling only reads main `rate_limit` / `rateLimit` quota windows.
   - `全额度` sampling ignores `code_review_rate_limit` / `codeReviewRateLimit` and `additional_rate_limits` / `additionalRateLimits`.
   - `全额度` sampling checks `primary_window` / `primaryWindow` and `secondary_window` / `secondaryWindow`, then classifies by `limit_window_seconds` / `limitWindowSeconds`.
   - Weekly quota windows have priority over monthly windows, 5-hour windows are ignored, and an invalid selected window does not fall back to another window.
   - Model normalization includes provider prefixes, `models/`, date suffixes, `(high)`, and known `gpt-5.6` variants.
   - Unknown models are not guessed; they increment `pricing_missing_count`.
   - Usage manager restart/unregister behavior is preserved for same-process `Run -> Shutdown -> Run`.
   - Supports importing sub2api top-level `accounts[]` Agent Identity JSON.
   - Uses the fixed auth mode `auth_mode = agentIdentity` for Agent Identity credentials.
   - Sends Agent Identity requests with `Authorization: AgentAssertion <base64url-json>`.
   - Agent Identity auth does not use normal OAuth refresh and does not call `oauth2.googleapis.com/token`.
   - Missing `task_id` or explicit `invalid_task_id` responses trigger task registration and write the new task back to the auth file.
   - Same email with different workspace does not overwrite existing auth files.
   - Successful Agent Identity requests remain on the OpenAI usage / full-quota accounting path.
   - Sensitive values must not appear in logs, panel output, or error responses: `agent_private_key`, `id_token`, `task_id`, `access_token`, `refresh_token`, API Key, or a complete AgentAssertion value.

2. `002-management-panel-local-ui.patch`
   - Restores `static/management.html`, which is ignored by the official source tree.
   - Keeps auth-files page size 100 and quota page size 100.
   - Keeps the separate 403 and 401 batch removal controls.
   - Keeps auth card OpenAI usage display fields visible even when values are zero.
   - Keeps the auth card `全额度` field visible as a compact OpenAI usage item.
   - Keeps the frontend label `估算费用`.
   - Keeps token/request/usage-missing/pricing-missing display on eligible OpenAI/Codex/API Key/openai-compatible auth cards.
   - Keeps UTF-8 syntax-valid management panel output.

3. `003-cpamc-panel-source-local-ui.patch`
   - Applies to `E:\CLIProxyAPI\.codex-sync\cpamc-panel-source`.
   - Preserves the source-level CPAMC panel customizations before future official panel refreshes.
   - Keeps auth-files page size 100 and quota page size 100 in source files.
   - Keeps the separate `403移除（N）` and `401移除（N）` controls in source files.
   - Keeps special status display for `无效认证` and `401认证失效`.
   - Keeps the compact auth-card OpenAI usage block with `估算费用`, token counters, missing counters, and `全额度`.
   - Keeps `估算费用` and `全额度` displayed with two decimal places when a numeric value exists.
   - Keeps the official auth-file `fileGrid` card layout and avoids the old stacked usage-grid layout.

## Secret Handling

Do not store real Google OAuth client credentials in source, generated patches, or
GitHub commits. `internal/api/handlers/management/api_tools.go` must read
Antigravity OAuth client credentials from auth metadata first, then from
`ANTIGRAVITY_OAUTH_CLIENT_ID` / `ANTIGRAVITY_OAUTH_CLIENT_SECRET`.

## Validation

The current queue was validated against a clean `v7.2.95` baseline with:

```powershell
git apply --cached --check E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
git apply --check --ignore-space-change --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

No backend executable was generated during the 2026-07-23 Agent Identity patch
queue update. If a future validation build is needed, the only allowed output is:

```text
E:\CLIProxyAPI\build-output\cli-proxy-api.exe
```

Do not overwrite `E:\CLIProxyAPI\cli-proxy-api.exe` during validation or future
updates unless the user explicitly asks.
