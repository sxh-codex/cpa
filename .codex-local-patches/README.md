# CLIProxyAPI Local Patch Queue

Base upstream: `v7.2.80`

This queue stores the local source customizations that must be restored after an
official CLIProxyAPI source refresh. It intentionally excludes runtime data:
`config.yaml`, auth directories, auth JSON files, `data`, caches, logs, and build
output.

Apply from a clean upstream source tree, in this order:

```powershell
git apply E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch
git apply E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

On Windows trees checked out with `core.autocrlf=true`, use this form if Git
reports whitespace-only context conflicts:

```powershell
git apply --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch
git apply --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
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

2. `002-management-panel-local-ui.patch`
   - Restores `static/management.html`, which is ignored by the official source tree.
   - Keeps auth-files page size 100 and quota page size 100.
   - Keeps the separate 403 and 401 batch removal controls.
   - Keeps auth card OpenAI usage display fields visible even when values are zero.
   - Keeps the auth card `全额度` field visible as a compact OpenAI usage item.
   - Keeps the frontend label `估算费用`.
   - Keeps token/request/usage-missing/pricing-missing display on eligible OpenAI/Codex/API Key/openai-compatible auth cards.
   - Keeps UTF-8 syntax-valid management panel output.

## Secret Handling

Do not store real Google OAuth client credentials in source, generated patches, or
GitHub commits. `internal/api/handlers/management/api_tools.go` must read
Antigravity OAuth client credentials from auth metadata first, then from
`ANTIGRAVITY_OAUTH_CLIENT_ID` / `ANTIGRAVITY_OAUTH_CLIENT_SECRET`.

## Validation

The current queue was validated against a clean `v7.2.80` baseline with:

```powershell
git apply --cached --check E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
git apply --check --ignore-space-change --ignore-whitespace E:\CLIProxyAPI\.codex-local-patches\001-backend-local-customizations.patch E:\CLIProxyAPI\.codex-local-patches\002-management-panel-local-ui.patch
```

The current build was produced at:

```text
E:\CLIProxyAPI\build-output\cli-proxy-api.exe
```

Do not overwrite `E:\CLIProxyAPI\cli-proxy-api.exe` during validation or future
updates unless the user explicitly asks.
