# kd · Data backup tool powered by App Keys

[中文](README.md)

[![GitHub release](https://img.shields.io/github/v/release/ejfkdev/kd?label=release)](https://github.com/ejfkdev/kd/releases)
[![GitHub downloads](https://img.shields.io/github/downloads/ejfkdev/kd/total)](https://github.com/ejfkdev/kd/releases)
[![GitHub Actions](https://img.shields.io/github/actions/workflow/status/ejfkdev/kd/release.yml)](https://github.com/ejfkdev/kd/actions)
[![Go version](https://img.shields.io/github/go-mod/go-version/ejfkdev/kd)](https://github.com/ejfkdev/kd)
[![License](https://img.shields.io/github/license/ejfkdev/kd)](LICENSE)

Back up every piece of business data reachable by an App Key/Secret or Access Token — contacts, group messages, docs, calendars, attendance and more — into structured JSON plus original attachments, with rate limiting, resumable downloads and incremental re-runs.

Currently supports **Feishu (Lark)** and **DingTalk**.

## Features

- **Auto-detect credentials**: `cli_*` → Feishu, `ding*` → DingTalk, `t-/u-` tokens → Feishu
- **Startup validation**: invalid credentials abort immediately; probes tell "invalid credential" apart from "no permission"
- **Permission mapping**: read app scopes first, then probe each API surface for the missing scope names
- **Full backup**: list APIs paged at their limits and merged; per-item loops parallelized
- **Cross-source ID harvest**: when a list API is denied, collect ids from messages/members/links and query by id instead
- **Output layout**: one JSON per module under per-business dirs; failures/empty results never written to data files, request-level log in run.log
- **Resource backup**: `source-id-name` naming, resumable downloads, image type sniffing for extensions
- **SDK first**: official SDKs where available, legacy endpoints as fallback

## Install

**Homebrew (macOS)**

```bash
brew install ejfkdev/tap/kd
```

**go install**

```bash
go install github.com/ejfkdev/kd/cmd/kd@latest
```

**Download release binaries** (no unpacking needed)

Pick the right `kd_<os>_<arch>` asset from [Releases](https://github.com/ejfkdev/kd/releases):

| Platform | Asset |
| --- | --- |
| Linux x64 / arm64 | `kd_linux_amd64` / `kd_linux_arm64` |
| macOS Intel / Apple Silicon | `kd_darwin_amd64` / `kd_darwin_arm64` |
| Windows x64 / arm64 | `kd_windows_amd64.exe` / `kd_windows_arm64.exe` |

```bash
VER=v0.1.0
curl -L -o kd https://github.com/ejfkdev/kd/releases/download/${VER}/kd_linux_amd64
chmod +x kd && ./kd version
```

> macOS binaries are unsigned and not UPX-compressed (modern macOS kills packed executables); if Gatekeeper blocks the first run, right-click → Open.

## Quick start

```bash
# credentials auto-detect the product
kd run -app-id cli_xxx -app-secret yyy
kd run -app-id dingxxx -app-secret yyy -proxy http://127.0.0.1:8080

# explicit product
kd feishu -token t-xxx
kd dingtalk -access-token <token>

# misc
kd list       # full module list (values for -only/-skip)
kd version
```

## Common flags

| Flag | Meaning |
| --- | --- |
| `-out <dir>` | output dir (default `feishu_dump_<ts>/` / `dingtalk_dump_<ts>/`) |
| `-qps` / `-workers` | global rate limit / per-item loop concurrency (default 20 / 8) |
| `-retry` / `-timeout` | rate-limit retries / per-request timeout seconds (5-300) |
| `-proxy` / `-x` | HTTP(S) proxy for all request paths |
| `-host feishu\|lark` (Feishu) | platform domain: `open.feishu.cn` / `open.larksuite.com`, default feishu |
| `-only a,b` / `-skip a,b` | module selection using `group.name` (see `kd list`) |
| `-no-download` / `-max-items N` | disable resource download / max items per list |
| `-verbose` | page/progress logging |

Feishu also has `-resume`, `-download-threads` and the `-cal-from/-cal-to` event window.

## Credentials

**Feishu**

| Form | Usage |
| --- | --- |
| App ID + App Secret | `-app-id cli_xxx -app-secret yyy` (tokens auto-fetched and cached) |
| tenant / user / app token | `-tenant-token t-xxx` / `-user-token u-xxx` / `-app-token t-xxx`, or `-token` auto-detect |
| OAuth authorization code | `-code <code> -code-redirect-uri <uri>` (exchanged for a user token) |

Env vars: `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_USER_TOKEN` / `FEISHU_TENANT_TOKEN`.

**DingTalk**

| Form | Usage |
| --- | --- |
| AppKey + AppSecret | `-app-key dingxxx -app-secret yyy` (`-app-id` is an alias) |
| access_token | `-access-token <token>` |

Env vars: `DINGTALK_APP_KEY` / `DINGTALK_APP_SECRET` / `DINGTALK_ACCESS_TOKEN`.

## Output layout

```
feishu_dump_<ts>/
├── meta.json                    # identity, scopes, stats/perf/request_codes
├── run.log                      # one line per request: path + http/business code
├── contact/users.json           # one file per module: {group, key, count, items|object}
├── im/chats/<chat_id>/*.json    # chat data split per chat (info/members/messages/reactions…)
├── docs/documents/<token>.json  # one JSON per document
└── resources/
    ├── resources.json           # resource index (written only on success)
    └── im/im-img_v3_xxx.jpg     # <source>-<id>-<name|extension>
```

Failures and empty results are never written as data; they land in run.log only.

## Modules

**Feishu**

| Group | Content |
| --- | --- |
| `identity` | user info, granted scopes, app scope profile, tenant info (into meta.json) |
| `contact.*` | auth scope, department tree, users/details, groups, custom fields, job levels, work cities, units |
| `im.*` | chats, per-chat info/members/announcement/pins, message history split by chat, reactions, attachments & images |
| `calendar.*` | calendars, events (time window), event details/attendees/ACLs, event attachments |
| `docs.*` | drive file tree (recursive), batch meta, comments, permissions, docx full text, sheet values, bitable data, wiki per-space |
| `task.*` `approval.*` `mail.*` `minutes.*` `okr.*` `hr.*` `misc.*` | tasks, approvals (v4), mail, minutes & transcripts, OKR, HR, bot info |

**DingTalk**

| Group | Content |
| --- | --- |
| `identity` | org auth info (SDK), auth/scopes ranges (into meta.json) |
| `contact.*` | department tree, users (parallel-merged), per-user details, scoped fallback, avatar download |
| `approval.*` | instance id list, instance details (SDK first, legacy fallback) |
| `attendance.*` | attendance groups, check-in records (SDK) |
| `misc.*` | app info, permission probe (per-API missing scope report) |

Full live list: `kd list`.

## License

[MIT](LICENSE) © ejfkdev