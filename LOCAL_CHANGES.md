# Local Changes

This file records the local customizations that should be restored after
updating CLIProxyAPI from upstream source or release packages.

Do not treat official features, normal configuration values, or release updates
as local changes. Only the custom behavior below needs to be preserved.

## 1. HTTP 429 Cooldown Behavior

Local requirement:

- Do not cool down an auth/account for the first 10 consecutive HTTP 429 errors.
- Start the original cooldown behavior on the 11th consecutive HTTP 429.
- If any request succeeds before the threshold is reached, clear the previous
  429 error count.

Current implementation file:

- `sdk/cliproxy/auth/conductor.go`

Important marker:

- `quota429CooldownThreshold = 11`

After an upstream update, verify this file still implements the local threshold
behavior before rebuilding.

## 2. Management Panel Page Size

Local requirement:

- Auth files page: show 100 files per page.
- Quota management page: show 100 files per page.

Current implementation file:

- `static/management.html`

This is a locally modified bundled management panel asset. It may be replaced
by upstream panel updates, so verify the page-size behavior after any update.

Expected local markers in `static/management.html`:

- Auth files max page size is 100.
- Auth files default page size is 100.
- Quota management page size is 100.

Useful checks:

```powershell
Select-String -Path .\static\management.html -Pattern 'hE=100,gE=100|max:100|i<3\|\|i>100|pw=100'
```

## 3. Management Panel Auto Update

Local requirement:

- Keep management panel auto-update disabled so the local page-size and button
  customizations are not overwritten automatically.

Current implementation file:

- `config.yaml`

Expected config:

```yaml
remote-management:
  disable-auto-update-panel: true
```

After replacing or regenerating `config.yaml`, confirm this setting remains
`true`.

## 4. Invalid Auth Removal Buttons

Local requirement:

- Keep the inactive-workspace 403 removal flow separate from invalidated-token
  401 removal.
- Keep the upload button immediately after the refresh button in the auth files
  header actions.
- The existing inactive-workspace status remains `无效认证`, but the management
  panel button label is `403移除（N）`.
- Add an independent `401移除（N）` button for auth files marked
  `401认证失效`.
- Move `401认证失效` files to
  `C:\Users\admin\.cli-proxy-api\removed\401-invalid\`.

Current implementation files:

- `internal/api/handlers/management/api_tools.go`
- `internal/api/handlers/management/auth_files.go`
- `internal/api/server.go`
- `sdk/cliproxy/auth/conductor.go`
- `static/management.html`

Expected matching rule for `401认证失效`:

- HTTP status is 401.
- `error.type` is `authentication_error`.
- `error.code` is `auth_unavailable`.
- `error.message` is exactly
  `Your authentication token has been invalidated. Please try signing in again.`

## 5. Fixed Build Output Directory

Local requirement:

- Compile release artifacts to `E:\CLIProxyAPI\build-output\cli-proxy-api.exe`.
- Do not overwrite the running root executable unless explicitly requested.

## Update Checklist

After updating upstream source or release files:

1. Restore the HTTP 429 cooldown customization in `sdk/cliproxy/auth/conductor.go`.
2. Restore the 100-per-page management panel customization in `static/management.html`.
3. Confirm `config.yaml` keeps `remote-management.disable-auto-update-panel: true`.
4. Restore the split `403移除（N）` / `401移除（N）` invalid-auth removal customization.
5. Build to `E:\CLIProxyAPI\build-output\cli-proxy-api.exe`.
6. Restart the running process manually when ready.
7. Open the management panel and use `Ctrl+F5` to bypass browser cache.
