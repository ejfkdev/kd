# kd · 基于应用 Key 的数据备份工具

[English](README.en.md)

[![GitHub release](https://img.shields.io/github/v/release/ejfkdev/kd?label=release)](https://github.com/ejfkdev/kd/releases)
[![GitHub downloads](https://img.shields.io/github/downloads/ejfkdev/kd/total)](https://github.com/ejfkdev/kd/releases)
[![GitHub Actions](https://img.shields.io/github/actions/workflow/status/ejfkdev/kd/release.yml)](https://github.com/ejfkdev/kd/actions)
[![Go version](https://img.shields.io/github/go-mod/go-version/ejfkdev/kd)](https://github.com/ejfkdev/kd)
[![License](https://img.shields.io/github/license/ejfkdev/kd)](LICENSE)

输入应用的 App Key/Secret 或 Access Token，把该凭据可访问的业务数据（通讯录、群消息、文档、日程、考勤等）**备份到本地**——结构化 JSON 加原始附件/图片，支持限流、断点续传与增量续跑。

当前支持 **飞书 (Feishu/Lark)**、**企业微信 (WeCom)** 与 **钉钉 (DingTalk)**。

## 特性

- **凭据自动识别**：`cli_*` → 飞书，`ww*/wx*` → 企业微信，`ding*` → 钉钉，`t-/u-` 前缀 token → 飞书，其余 token → 钉钉
- **启动即校验**：凭据无效立即中止；探针区分「凭据无效」与「接口无权限」
- **权限边界测绘**：先查应用授权范围，逐接口探测缺失权限并解析出具体 scope 名
- **全量备份**：列表按接口上限翻页、结果合并；逐条循环并行（workers 可配）
- **跨源 ID 收集**：列表接口被禁用时，从消息/群成员/链接等处收集 id，再走按 id 查询接口
- **输出约定**：按业务分目录、每模块一个 JSON；空结果/失败不落盘，请求级日志进 run.log
- **资源备份**：附件/头像/图片按 `来源-id-原名` 命名，断点续传，自动识别图片类型补扩展名
- **SDK 优先**：官方 SDK 覆盖的接口走 SDK，未覆盖的自动回退老接口

## 安装

**Homebrew（macOS）**

```bash
brew install ejfkdev/tap/kd
```

**go install**

```bash
go install github.com/ejfkdev/kd/cmd/kd@latest
```

**下载 Release 二进制**（无需解包，直接可执行）

从 [Releases](https://github.com/ejfkdev/kd/releases) 按平台选取 `kd_<os>_<arch>` 资产：

| 平台 | 资产名 |
| --- | --- |
| Linux x64 / arm64 | `kd_linux_amd64` / `kd_linux_arm64` |
| macOS Intel / Apple Silicon | `kd_darwin_amd64` / `kd_darwin_arm64` |
| Windows x64 / arm64 | `kd_windows_amd64.exe` / `kd_windows_arm64.exe` |

```bash
VER=v0.1.0
curl -L -o kd https://github.com/ejfkdev/kd/releases/download/${VER}/kd_linux_amd64
chmod +x kd && ./kd version
```

> macOS 二进制未签名且不做 UPX（现代 macOS 会终止加壳的可执行文件），首次运行如被 Gatekeeper 拦截请右键打开。

## 快速开始

```bash
# 凭据自动识别，无需指定产品
kd run -app-id cli_xxx -app-secret yyy
kd run -app-id wwxxx -app-secret yyy
kd run -app-id dingxxx -app-secret yyy -proxy http://127.0.0.1:8080

# 显式指定产品
kd feishu -token t-xxx
kd wecom -corpid wwxxx -corpsecret yyy
kd dingtalk -access-token <token>

# 其他命令
kd list       # 全部数据模块清单（-only/-skip 取值）
kd version
```

## 公共参数

| 参数 | 说明 |
| --- | --- |
| `-out <dir>` | 输出目录（默认 `feishu_dump_<ts>/` / `wecom_dump_<ts>/` / `dingtalk_dump_<ts>/`） |
| `-qps` / `-workers` | 全局限速 / 逐条循环并发（默认 20 / 8，接口级余量自动 sleep 兜底） |
| `-retry` / `-timeout` | 限流重试次数 / 单请求超时秒（5-300） |
| `-proxy` / `-x` | HTTP(S) 代理，作用于全部请求通路 |
| `-host feishu\|lark`（飞书） | 平台域：飞书 `open.feishu.cn` / Lark 国际站 `open.larksuite.com`，默认 feishu |
| `-only a,b` / `-skip a,b` | 模块选择，用 `组.名`（见 `kd list`） |
| `-no-download` / `-max-items N` | 关闭资源下载 / 每列表条目上限 |
| `-verbose` | 逐页/进度日志 |

飞书另有 `-resume`（跳过已完成模块）、`-download-threads`、`-cal-from/-cal-to` 日程窗口。

## 凭据形态

**飞书**

| 形态 | 用法 |
| --- | --- |
| App ID + App Secret | `-app-id cli_xxx -app-secret yyy`（内部接口自动换 token 并缓存续期） |
| tenant / user / app token | `-tenant-token t-xxx` / `-user-token u-xxx` / `-app-token t-xxx`，或 `-token` 自动识别 |
| OAuth 授权码 | `-code <code> -code-redirect-uri <uri>`（换 user token） |

环境变量：`FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_USER_TOKEN` / `FEISHU_TENANT_TOKEN`。

**企业微信**

| 形态 | 用法 |
| --- | --- |
| CorpID + Secret | `-corpid wwxxx -corpsecret yyy`（`-app-id`/`-app-secret` 为等价别名；自动换 token 并缓存续期） |
| access_token | `-access-token <token>` |

环境变量：`WECOM_CORP_ID` / `WECOM_CORP_SECRET` / `WECOM_ACCESS_TOKEN`。

> 企业微信各业务面（通讯录/客户联系/审批/打卡/会议）各用各的 secret：通讯录同步助手、审批应用、打卡应用等凭据可见的数据面不同，工具会逐面探测并如实记录被拒原因。

**钉钉**

| 形态 | 用法 |
| --- | --- |
| AppKey + AppSecret | `-app-key dingxxx -app-secret yyy`（`-app-id` 为等价别名） |
| access_token | `-access-token <token>` |

环境变量：`DINGTALK_APP_KEY` / `DINGTALK_APP_SECRET` / `DINGTALK_ACCESS_TOKEN`。

## 输出结构

```
feishu_dump_<ts>/
├── meta.json                    # 身份、授权范围、stats/perf/request_codes 画像
├── run.log                      # 每个请求的方法/路径/HTTP码/业务码+msg
├── contact/users.json           # 每模块一个文件：{group, key, count, items|object}
├── im/chats/<chat_id>/*.json    # 群数据按群拆目录（info/members/messages/reactions…）
├── docs/documents/<token>.json  # 文档类资源一文件一 JSON
└── resources/
    ├── resources.json           # 资源索引（成功才生成）
    └── im/im-img_v3_xxx.jpg     # 资源文件：<来源>-<id>-<原名|扩展名>
```

失败与空结果不写入任何数据 JSON，全部落在 run.log。

## 数据模块

**飞书**

| 模块组 | 备份内容 |
| --- | --- |
| `identity` | 用户信息、granted scopes、应用 scope 画像、租户信息（进 meta.json） |
| `contact.*` | 授权范围、部门树、用户列表/逐用户详情、用户组、自定义字段、职务序列/级别、工作城市、单位 |
| `im.*` | 群列表、群详情/成员/公告/置顶、历史消息（按群拆分）、表情回复、消息附件与图片 |
| `calendar.*` | 日历/主日历、日程（时间窗口）、日程详情/参与人/ACL、日程附件 |
| `docs.*` | 云空间文件树（递归）、批量元信息、评论、权限、docx 全文、表格值、多维表格全量、知识库（按空间拆目录） |
| `task.*` `approval.*` `mail.*` `minutes.*` `okr.*` `hr.*` `misc.*` | 任务与附件、审批(v4)、邮件、妙记与转写、OKR、人事、机器人信息 |

**企业微信**

| 模块组 | 备份内容 |
| --- | --- |
| `identity` | corpid、可见应用列表（进 meta.json） |
| `agent.*` | 可见应用列表、逐应用详情（含 logo 下载） |
| `contact.*` | 部门树（simplelist 回退）、成员列表（user/listid 回退）、逐成员详情、标签及标签成员、头像下载 |
| `external.*` | 客户联系功能成员、企业标签库、联系我渠道、客户列表/详情、客户群列表/详情、离职待继承、朋友圈（近一年） |
| `approval.*` | 表单模板、审批单号（近一年按月窗口）、逐单详情 |
| `checkin.*` | 成员打卡规则、日报/月报数据（近 90 天） |
| `oa.*` | 会议室列表、成员直播 ID/直播详情 |
| `misc.*` | 权限边界探测（逐接口记录无权限原因） |

**钉钉**

| 模块组 | 备份内容 |
| --- | --- |
| `identity` | 企业授权信息（SDK）、auth/scopes 授权范围（进 meta.json） |
| `contact.*` | 部门树、用户列表（并行合并）、逐用户详情、授权范围回退、头像下载 |
| `approval.*` | 实例 id 列表、实例详情（SDK 优先，老接口回退） |
| `attendance.*` | 考勤组、打卡记录（SDK） |
| `misc.*` | 应用信息、权限边界探测（逐接口返回缺失 scope 清单） |

完整清单与实时模块表：`kd list`。

## License

[MIT](LICENSE) © ejfkdev