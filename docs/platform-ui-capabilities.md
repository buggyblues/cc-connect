# IM Platform Card/UI Component Capabilities

> 调研日期: 2026-05-15
>
> 本文档详细对比各 IM 平台在卡片消息、自定义 UI、交互组件等方面的能力，包含 API 设计、交互流程和实现对比。

---

## 目录

1. [cc-connect 核心抽象层](#cc-connect-core)
2. [Feishu / Lark](#1-feishu--lark-飞书)
3. [Discord](#2-discord)
4. [Slack](#3-slack)
5. [Telegram](#4-telegram)
6. [DingTalk](#5-dingtalk-钉钉)
7. [WeCom](#6-wecom-企业微信)
8. [QQ Bot](#7-qq-bot)
9. [LINE](#8-line)
10. [MAX](#9-max)
11. [Shadow OB](#10-shadow-ob)
12. [Weixin / QQ / Weibo](#11-其他平台)
13. [能力矩阵总览](#能力矩阵总览)
14. [实现建议](#实现建议)

---

## <a id="cc-connect-core"></a>cc-connect 核心抽象层

cc-connect 通过 Go 接口定义了三个层级的卡片/UI 能力，各平台按能力选择性实现：

### 核心数据结构 `core.Card`

```
Card
├── Header { Title, Color }        // 彩色标题栏
└── Elements[]                      // 内容元素列表
    ├── CardMarkdown { Content }    // Markdown 文本
    ├── CardDivider {}              // 分隔线
    ├── CardActions { Buttons[], Layout }  // 按钮组 (row / equal_columns)
    ├── CardListItem { Text, BtnText, BtnType, BtnValue, Extra }
    ├── CardSelect { Placeholder, Options[], InitValue }
    └── CardNote { Text, Tag }      // 脚注文本
```

### 能力接口层级

| 接口 | 方法 | 说明 |
|------|------|------|
| **CardSender** | `SendCard()`, `ReplyCard()` | 发送完整结构化卡片 |
| **CardNavigable** | `SetCardNavigationHandler()` | 卡片内导航/按钮回调（原地更新） |
| **CardRefresher** | `RefreshCard()` | 异步刷新已发送的卡片 |
| **RichCardSupporter** | `BuildRichCard()` | 构建富进度卡片（thinking/working/done/error） |
| **InlineButtonSender** | `SendWithButtons()` | 发送文本 + 内联按钮行（非卡片平台降级方案） |
| **PreviewStarter** | `SendPreviewStart()` | 开启流式预览消息 |
| **PreviewCleaner** | `DeletePreviewMessage()` | 清理预览消息 |
| **PreviewStatusUpdater** | `SetPreviewStatus()` | 更新预览卡片状态色 |
| **MessageUpdater** | `UpdateMessage()` | 原地编辑已发送消息 |
| **FormattingInstructionProvider** | `FormattingInstructions()` | 平台特有的 Markdown 语法指导 |
| **CommandRegistrar** | `RegisterCommands()` | 注册平台原生斜杠命令菜单 |

### 引擎分发策略

```
1. RichCardSupporter? → BuildRichCard (thinking/working/done 进度卡片)
2. InlineButtonSender? + HasButtons → SendWithButtons (文本 + 内联按钮)
3. CardSender? → ReplyCard/SendCard (完整卡片渲染)
4. Fallback → Card.RenderText() (纯文本降级)
```

---

## 1. Feishu / Lark (飞书)

### 当前实现状态: ★★★★★ 全功能（最完整）

**已实现接口**: `CardSender`, `CardNavigable`, `CardRefresher`, `RichCardSupporter`, `PreviewStatusUpdater`, `ImageSender`, `FileSender`, `TypingIndicator`, `TypingIndicatorDone`

### 原生卡片系统: 消息卡片 (Message Card) v1

#### 卡片结构

```json
{
  "config": { "wide_screen_mode": true },
  "header": {
    "title": { "tag": "plain_text", "content": "标题" },
    "template": "blue"  // blue|green|red|orange|purple|grey|turquoise|violet|indigo|wathet|yellow|carmine
  },
  "elements": [ ... ]
}
```

#### 支持的组件 (卡片构建器)

| 组件 Tag | cc-connect 映射 | 说明 |
|----------|----------------|------|
| `markdown` | CardMarkdown | Markdown 文本块，支持飞书扩展语法 |
| `hr` | CardDivider | 水平分隔线 |
| `button` | CardButton | 按钮 (primary/default/danger)，支持 `width: "fill"` |
| `action` | CardActions (row) | 按钮容器（水平排列） |
| `column_set` | CardActions (equal_columns) / CardListItem | 多列布局，支持 `flex_mode: "bisect"` |
| `select_static` | CardSelect | 静态下拉选择器 |
| `note` | CardNote | 小字脚注 |
| `form` | 特殊处理 (delete_mode_form) | 表单容器，内含 checker、submit button |
| `checker` | 特殊处理 | 复选框组件，支持 checked 状态 |
| `image` | 未映射 | 图片组件 |
| `multi_image` | 未映射 | 多图组件 |
| `date_picker` | 未映射 | 日期选择器 |
| `overflow` | 未映射 | 折叠菜单 |
| `person` | 未映射 | @人组件 |

#### 交互能力

| 能力 | 实现状态 | 说明 |
|------|---------|------|
| 按钮回调 | ✅ 已实现 | `card.action.trigger` 事件 → `CardNavigationHandler` |
| 原地卡片更新 | ✅ 已实现 | `PATCH /im/v1/messages/{message_id}` → `RefreshCard()` |
| 表单提交 | ✅ 已实现 (delete mode) | `form_action_type: "submit"` 触发回调 |
| 下拉选择回调 | ✅ 已实现 | 通过 `card.action.trigger` |
| 宽屏模式 | ✅ 已实现 | `wide_screen_mode: true` |
| 便捷更新 (v2) | ❌ 未实现 | `PATCH /im/v1/messages/{message_id}/sections` 增量更新卡片片段 |
| 模板卡片 | ❌ 未实现 | 飞书模板卡片 (template_id) 可复用卡片布局 |
| URL 预览 | ❌ 未实现 | 自动展开链接为卡片预览 |

#### 进度卡片 (RichCard)

`BuildRichCard()` 生成可视化代理执行进度：

```
┌──────────────────────────────────┐
│ 🔵 正在处理...                    │ ← header (status 着色)
├──────────────────────────────────┤
│ 📝 思考                           │ ← ToolStep (thinking)
│ 🔧 Bash: npm install              │ ← ToolStep (tool)
│ ✅ Bash: npm test (passed)        │ ← ToolStep (done)
│ 📄 result.md                      │ ← markdown content
├──────────────────────────────────┤
│ ⏱ 12.5s                          │ ← elapsed footer
└──────────────────────────────────┘
```

#### 回调处理流程

```
用户点击按钮
  → Feishu POST card.action.trigger
  → feishu.handleCardActionTrigger()
  → 提取 action + session_key
  → CardNavigationHandler(action, sessionKey)
  → 返回新 Card
  → cardActionResponse(ctx, newCard)   // 同步原地更新
  → 或 异步 RefreshCard(sessionKey, newCard)  // delete_mode 等异步场景
```

#### 未实现的飞书原生能力

- **消息卡片 v2 (Open ID)**: 新版本卡片 API，支持更灵活的布局
- **模板卡片**: 服务端定义卡片模板，客户端通过 `template_id` 引用
- **便捷更新**: 增量更新卡片片段而非整张卡片
- **URL 预览**: 自动将消息中的链接渲染为卡片
- **`multi_image` / `image`**: 卡片内嵌图片
- **`date_picker` / `overflow`**: 更多交互组件
- **`person`**: @人组件

---

## 2. Discord

### 当前实现状态: ★★★☆☆ 按钮 + 进度卡片

**已实现接口**: `InlineButtonSender`, `ImageSender`, `FileSender`, `MessageUpdater`, `PreviewStarter`, `PreviewCleaner`, `TypingIndicator`, `CommandRegistrar`, `ChannelNameResolver`

**未实现**: `CardSender`, `CardNavigable`, `CardRefresher`

### 原生 API 能力

#### Message Components v2 组件体系

Discord 在 2025 年推出了 **Components v2** (需在消息中设置 flag `1 << 15`)，将组件分为三类：

**布局组件 (Layout)**

| 组件 | ID | 说明 |
|------|----|----|
| Action Row | 1 | 容器，最多 5 个按钮或 1 个选择器 |
| Section | 9 | 文本 + 附件（按钮/缩略图）组合 |
| Container | 17 | 视觉分组容器 |
| Separator | 14 | 垂直间距分隔 |
| Label | 18 | Modal 中组件的标签/描述容器 |

**内容组件 (Content)**

| 组件 | ID | 说明 |
|------|----|----|
| Text Display | 10 | Markdown 文本渲染，替代旧版 content |
| Media Gallery | 12 | 1-10 个媒体附件画廊 |
| Thumbnail | 11 | 小图 (GIF/WEBP)，仅 Section 的 accessory |
| File | 13 | 附件文件展示 |

**交互组件 (Interactive)**

| 组件 | ID | 可用位置 | 说明 |
|------|----|----|------|
| Button | 2 | Message, Modal | 6 种样式 (Primary/Secondary/Success/Danger/Link/Premium) |
| String Select | 3 | Message, Modal | 25 个选项，支持 min/max values |
| User Select | 5 | Message, Modal | 自动填充服务器用户列表 |
| Role Select | 6 | Message, Modal | 自动填充服务器角色列表 |
| Channel Select | 8 | Message, Modal | 自动填充频道列表，可按类型过滤 |
| Mentionable Select | 7 | Message, Modal | 用户 + 角色合并选择器 |
| Text Input | 4 | Modal only | Short/Paragraph 两种样式，最长 4000 字符 |
| File Upload | 19 | Modal | 文件上传组件 |
| Radio Group | 21 | Modal | 单选按钮组 |
| Checkbox Group | 22 | Modal | 多选复选框组 |
| Checkbox | 23 | Modal | 单个复选框 |

#### 当前实现与能力差距

| Discord 原生能力 | cc-connect 实现 | 差距 |
|---|---|---|
| Embed (富嵌入) | ✅ progress.go 实现了 Embed | 仅用于进度卡片，未用于通用卡片 |
| Button | ✅ SendWithButtons | 仅用于命令交互，非通用按钮 |
| String Select | ❌ 未实现 | 可用于模型/模式切换 |
| Modal (弹窗) | ❌ 未实现 | 可用于表单输入（如 /bind 配置） |
| Section Layout | ❌ 未实现 | 可用于卡片式消息布局 |
| Components v2 | ❌ 未实现 | 新的布局系统，可替代 Embed |

#### Embeds vs Components 对比

| 特性 | Embed | Components v2 |
|---|---|---|
| 最大内容 | 6000 字符 | 40 个组件 |
| 布局能力 | 固定字段 (title/description/fields/footer) | 灵活容器组合 |
| 交互能力 | 不支持（仅装饰性） | 支持按钮、选择器、模态框 |
| 颜色 | sidebar color (integer) | 组件级样式 |
| 图片 | thumbnail + image | Thumbnail + Media Gallery |

### 建议实现

```
优先级 1: 实现 CardSender → 使用 Embed + Action Row 组合
优先级 2: 实现 String Select → 模型切换 / 模式切换
优先级 3: 实现 Modal → /bind 等配置表单
```

---

## 3. Slack

### 当前实现状态: ★☆☆☆☆ 纯文本

**已实现接口**: `ImageSender`, `FileSender`, `TypingIndicator`, `ChannelNameResolver`, `FormattingInstructionProvider`

**未实现**: `CardSender`, `InlineButtonSender`, `CardNavigable`, `PreviewStarter`

### 原生 Block Kit API

Slack 拥有业界最成熟的 Block Kit 体系，支持 18 种 Block 类型：

#### Block 类型

| Block | 说明 | 适用场景 |
|---|---|---|
| **Actions** | 交互组件容器（button, select, datepicker...） | 按钮组、选择器 |
| **Alert** | 通知/警告样式块 | 系统告警 |
| **Card** | 卡片布局 | 结构化展示 |
| **Carousel** | 水平滑动卡片集 | 多项选择 |
| **Context** | 辅助小字文本 | 元信息展示 |
| **Context Actions** | 上下文中嵌入操作 | 紧凑交互 |
| **Divider** | 分隔线 | 视觉分隔 |
| **File** | 文件预览 | 文件展示 |
| **Header** | 大号标题文本 | 卡片标题 |
| **Image** | 图片 + 可选文本 | 图片展示 |
| **Input** | 输入组件（Modal 内） | 表单输入 |
| **Markdown** | 格式化 Markdown | 文本内容 |
| **Plan** | 计划/排期展示 | 订阅信息 |
| **Rich Text** | 富文本（粗体/斜体/列表等） | 格式化内容 |
| **Section** | 通用块（文本 + accessory） | 通用内容 |
| **Table** | 表格数据 | 数据展示 |
| **Task Card** | 任务卡片（复选框/状态） | 任务管理 |
| **Video** | 视频嵌入 | 视频播放 |

#### 交互组件 (可在 Actions Block 中使用)

| 组件 | 说明 |
|---|---|
| button | 按钮 (primary/danger/default) |
| static_select | 静态下拉选择器 |
| external_select | 外部数据源选择器 |
| users_select | 用户选择器 |
| conversations_select | 对话（频道/私信）选择器 |
| channels_select | 公频选择器 |
| datepicker | 日期选择器 |
| timepicker | 时间选择器 |
| overflow | 折叠菜单 |
| radio_buttons | 单选按钮组 |
| checkboxes | 复选框组 |
| multi_select 系列 | 对应各 select 的多选版本 |

#### Modal / Home Tab

| 表面 | 最大 Block 数 | 说明 |
|---|---|---|
| Message | 50 blocks | 消息内展示 |
| Modal | 100 blocks | 弹窗（需 trigger_id） |
| Home Tab | 100 blocks | App 首页 |

#### 与 cc-connect 的映射可行性

```
core.Card → Slack Block Kit 映射:
CardHeader     → Header block
CardMarkdown   → Section block (mrkdwn)
CardDivider    → Divider block
CardActions    → Actions block
CardButton     → button element
CardSelect     → static_select element
CardListItem   → Section block + accessory button
CardNote       → Context block
```

### 当前状态与建议

Slack 是 Tier 3 平台中 **原生能力最强的**，但 cc-connect 当前实现仅停留在纯文本。Block Kit 的模块化设计与 `core.Card` 抽象高度兼容。

**建议实现优先级**:
1. `CardSender` — Block Kit 映射 (约 300 行)
2. `InlineButtonSender` — Actions Block + Button (约 150 行)
3. `CardNavigable` — Block Action 回调 (约 200 行)
4. Modal — 配置表单 (约 300 行)

---

## 4. Telegram

### 当前实现状态: ★★★☆☆ 按钮 + 预览

**已实现接口**: `InlineButtonSender`, `ImageSender`, `FileSender`, `AudioSender`, `MessageUpdater`, `PreviewStarter`, `PreviewCleaner`, `TypingIndicator`, `CommandRegistrar`

**未实现**: `CardSender`, `CardNavigable`, `CardRefresher`（不需要，Telegram 无原生卡片概念）

### 原生 API 能力

#### 消息类型

| 类型 | 支持 |
|---|---|
| Text | ✅ HTML/MarkdownV2 格式化 |
| Photo | ✅ 支持 caption |
| Video | ✅ 支持 caption |
| Animation (GIF) | ✅ |
| Audio | ✅ 标题 + 表演者 |
| Document | ✅ 文件发送 |
| Voice | ✅ OGG/OPUS |
| Video Note | ✅ 圆形视频 |
| Poll | ✅ 投票 (quiz/regular) |
| Checklist | ✅ 清单 |
| Location | ✅ 位置 |
| Venue | ✅ 地点 |
| Contact | ✅ 联系人 |
| Dice | ✅ 骰子/飞镖 |
| Paid Media | ✅ 付费媒体 |

#### 交互组件

**1. Inline Keyboard (内联键盘)**

```json
{
  "inline_keyboard": [
    [
      { "text": "确认", "callback_data": "cmd:/confirm" },
      { "text": "取消", "callback_data": "cmd:/cancel" }
    ],
    [
      { "text": "打开网页", "url": "https://example.com" }
    ]
  ]
}
```

| 按钮类型 | 说明 |
|---|---|
| callback | 触发 callback_query | 
| url | 打开 URL |
| switch_inline_query | 切换到 inline 查询 |
| switch_inline_query_current_chat | 在当前聊天切换 |
| web_app | 打开 Mini App |
| login_url | 登录按钮 |

**限制**: `callback_data` 最大 64 字节（Telegram 的硬限制）

**2. Reply Keyboard (自定义键盘)**

| 能力 | 说明 |
|---|---|
| KeyboardButton | 自定义键盘按钮 |
| request_contact | 请求用户手机号 |
| request_location | 请求用户位置 |
| request_poll | 请求创建投票 |
| web_app | 打开 Web App |

**3. Web App / Mini App**

Bot 可以附加一个完整的 Web 应用：
- 通过 `web_app` 按钮触发
- 支持 `MainButton`, `BackButton`, `SettingsButton`
- 完整的 Telegram 主题适配
- 支持 CloudStorage, HapticFeedback, BiometricManager

#### 当前实现与能力差距

| Telegram 原生能力 | cc-connect 实现 | 差距 |
|---|---|---|
| Inline Keyboard | ✅ SendWithButtons | 功能完整 |
| callback_data 64 字节限制 | ✅ 截断处理 | — |
| Reply Keyboard | ❌ 未实现 | 可用于快捷命令 |
| Poll | ❌ 未实现 | 可用于投票确认 |
| Web App | ❌ 未实现 | 可用于富交互表单 |
| MarkdownV2 解析 | ✅ 使用 HTML parse mode | 可升级为 MarkdownV2 |

#### 回调处理流程

```
用户点击按钮
  → Telegram 发送 CallbackQuery
  → answerCallbackQuery (消弹窗提示)
  → 解析 callback_data 前缀:
    "cmd:"  → 命令回调（更新消息文本）
    "askq:" → 问答回调（追加选择到消息）
    "perm:" → 权限回调
  → 清除 inline keyboard (set inline_keyboard = [])
```

### 建议实现

```
优先级 1: Poll 消息 → 权限确认 / 多选问题
优先级 2: Reply Keyboard → 快捷指令菜单
优先级 3: Web App → Agent 配置面板
```

---

## 5. DingTalk (钉钉)

### 当前实现状态: ★☆☆☆☆ 纯 Markdown 文本

**已实现接口**: `ImageSender`, `FileSender`, `AudioSender`

**未实现**: `CardSender`, `InlineButtonSender`, 所有交互能力

### 原生 API 能力

#### 消息类型

| 类型 | 说明 |
|---|---|
| text | 纯文本 |
| markdown | Markdown 格式 (通过 session webhook) |
| image | 图片 (media_id) |
| file | 文件 (media_id) |
| voice | 语音 (media_id) |
| link | 链接消息 (title + text + picUrl + messageUrl) |
| oa | OA 审批消息 |
| **actionCard** | 交互卡片 (整体跳转/独立按钮) |
| **feedCard** | 多图文 Feed 卡片 |
| **互动卡片 (Interactive Card)** | 完整交互卡片平台 |

#### 互动卡片 (Interactive Card) — 未在 cc-connect 中实现

钉钉在 2023-2024 年大幅升级了互动卡片能力：

**卡片类型**:
- 轻量级互动卡片: 简单交互
- 流式 AI 卡片 (Typewriter Effect): 打字机效果、渐进式渲染
- 复杂布局卡片: 容器嵌套
- 数据面板卡片: 数据可视化
- 循环渲染卡片: 列表动态生成

**交互能力**:
- 按钮回调 (事件链式交互)
- 下拉选择器
- 表单提交
- 动态内容更新 (变量替换)
- 三态支持 (loading/success/error)
- 暗黑模式
- 国际化支持

**Workbench (工作台)**:
- 自建互动卡片嵌入工作台
- 组件间通信
- Dashboard 数据源绑定
- JSAPI 调用 (打开应用/链接/调用工作台 API)

#### 行动卡片 (ActionCard) — 轻量级交互

```json
// 整体跳转型
{
  "msgtype": "actionCard",
  "actionCard": {
    "title": "标题",
    "text": "markdown 内容",
    "singleTitle": "阅读全文",
    "singleURL": "https://example.com"
  }
}

// 独立按钮型
{
  "msgtype": "actionCard",
  "actionCard": {
    "title": "标题", 
    "text": "markdown 内容",
    "btnOrientation": "0",  // 0=垂直, 1=水平
    "btns": [
      { "title": "确认", "actionURL": "dingtalk://..." },
      { "title": "取消", "actionURL": "dingtalk://..." }
    ]
  }
}
```

**限制**: ActionCard 的按钮只能跳转 URL，**不支持回调**（无法触发服务端事件）。

#### 当前实现与能力差距

钉钉是 Tier 3 中 **原生互动能力最丰富的平台之一**，但 cc-connect 仅实现了基础 Markdown：

| 能力 | 实现状态 |
|---|---|
| markdown 文本 | ✅ 通过 session webhook |
| ActionCard 整体跳转 | ❌ 可用于链接分享 |
| ActionCard 独立按钮 | ❌ 可用于简单交互 |
| 互动卡片 | ❌ 完整卡片系统未接入 |
| AI 流式卡片 | ❌ 适合 cc-connect 进度展示 |
| 变量替换 | ❌ 动态内容更新 |

### 建议实现

```
优先级 1: ActionCard → 轻量级交互 (按钮 URL 跳转)
优先级 2: 互动卡片 → CardSender 实现 (映射 core.Card)
优先级 3: AI 流式卡片 → RichCardSupporter 实现
```

---

## 6. WeCom (企业微信)

### 当前实现状态: ★☆☆☆☆ 纯文本/可选 Markdown

**已实现接口**: `ImageSender`, `ReplyContextReconstructor`

**未实现**: 全部卡片和交互能力

### 原生 API 能力

#### 消息类型 (11 种)

| 类型 | 说明 | 交互 |
|------|------|------|
| text | 纯文本 + `<a>` 链接 | ❌ |
| image | 图片 (media_id) | ❌ |
| voice | 语音 (media_id) | ❌ |
| video | 视频 | ❌ |
| file | 文件 (media_id) | ❌ |
| textcard | 文本卡片 (title/description/url) | ✅ 点击跳转 |
| news | 图文消息 (最多 8 条) | ✅ URL/小程序跳转 |
| mpnews | 富图文消息 | ❌ |
| markdown | Markdown 子集 (2048 bytes) | ❌ |
| miniprogram_notice | 小程序通知 | ✅ 跳转小程序 |
| **template_card** | 模板卡片 (5 种子类型) | ✅ 丰富交互 |

#### 模板卡片 (Template Card) — 核心交互能力

5 种子类型，包含完整的组件体系：

**子类型概览**

| 类型 | 能力 |
|---|---|
| `text_notice` | 纯展示：标题/引用/强调/水平列表/跳转 |
| `news_notice` | 图文展示：卡片图片/图文区域/垂直列表 |
| **`button_interaction`** | 交互：最多 6 个按钮 + 1 个下拉选择器 |
| **`vote_interaction`** | 投票：单选/多选 checkbox (最多 20 项) + 提交按钮 |
| **`multiple_interaction`** | 多选：最多 3 个下拉选择器 (每个最多 10 选项) + 提交按钮 |

**模板卡片组件清单**

| 组件 | 适用类型 | 说明 |
|------|---------|------|
| `source` | 全部 | 来源图标 + 描述 + 颜色 (0=灰,1=黑,2=红,3=绿) |
| `main_title` | 全部 | 主标题 + 可选描述 |
| `action_menu` | text/notice/news/button | 右上角更多操作按钮 (1-3 个) |
| `quote_area` | text/notice/news/button | 引用区域，支持点击跳转 |
| `emphasis_content` | text_notice | 关键数据展示 |
| `sub_title_text` | text_notice, button | 二级纯文本 |
| `horizontal_content_list` | text/news/button | 键值对行 (type 0=文本,1=URL跳转,2=附件下载,3=成员详情) |
| `image_text_area` | news_notice | 左图右文样式 |
| `card_image` | news_notice | 卡片大图 (支持 aspect_ratio 1.3-2.25) |
| `vertical_content_list` | news_notice | 垂直内容列表 (最多 4 条) |
| `jump_list` | text/notice/news | 跳转链接列表 |
| `card_action` | text/notice/news/button | 整卡点击行为 (URL/小程序) |
| `button_list` | button_interaction | 按钮列表: type=0 回调 / type=1 URL; styles 1-4 |
| `button_selection` | button_interaction | 下拉选择器 + 按钮 |
| `checkbox` | vote_interaction | 选项列表 (mode 0=单选,1=多选), is_checked 默认选中 |
| `select_list` | multiple_interaction | 下拉选择器列表 (最多 3 个) |
| `submit_button` | vote/multiple | 提交按钮 (触发回调) |
| `task_id` | 全部有交互的 | 任务 ID (配合 action_menu 必填) |

#### 交互流程

```
发送交互卡片 → 用户操作 (点击/选择/提交)
  → 企业微信 POST 回调事件到配置的 URL
  → 返回 response_code (72h 有效, 只能用一次)
  → 使用 response_code 调用更新卡片 API
```

**回调事件类型**:
- `template_card_event`: 按钮点击 (`button_interaction`)
- `template_card_event`: 投票提交 (`vote_interaction`)
- `template_card_event`: 多项选择提交 (`multiple_interaction`)
- `template_card_event`: action_menu 操作

#### 当前实现与能力差距

| 能力 | 实现状态 |
|---|---|
| text/markdown | ✅ (`enableMarkdown=true`) |
| textcard | ❌ 可用于简单卡片 |
| news | ❌ 图文消息 |
| template_card (button_interaction) | ❌ 核心交互能力未接入 |
| template_card (vote/multiple) | ❌ 选择交互未接入 |
| 回调事件处理 | ❌ |
| 卡片更新 (response_code) | ❌ |

### 建议实现

```
优先级 1: template_card (button_interaction) → 按钮交互卡片
优先级 2: 回调事件处理 → CardNavigable + CardRefresher
优先级 3: template_card (vote/multiple) → 选择交互
```

---

## 7. QQ Bot

### 当前实现状态: ★☆☆☆☆ 纯文本/可选 Markdown

**已实现接口**: `ImageSender`, `FileSender`, `ReplyContextReconstructor`

**未实现**: 全部卡片和交互能力

### 原生 API 能力 (QQ Bot API v2)

#### 消息类型

| msg_type | 类型 | 说明 |
|---|---|---|
| 0 | 文本 | 纯文本 |
| 1 | 图文混排 | 文本 + 图片 |
| 2 | Markdown | Markdown 格式 |
| 3 | ARK | ARK 模板消息（结构卡片） |
| 4 | Embed | 嵌入式消息 |
| 7 | Media | 富媒体消息 |
| 9 | Keyboard | 带自定义键盘的消息 |

#### ARK 消息 (Ark Message)

QQ 的卡片式消息系统，通过 JSON 模板定义：

- ARK 23 (链接分享型): 标题 + 描述 + 缩略图 + 跳转链接
- ARK 24 (大图型): 大图预览
- ARK 37 (小程序型): 小程序跳转

限制: ARK 只能跳转，**不支持服务端回调**。

#### Keyboard 消息

自定义键盘，分为：

| 类型 | 说明 |
|---|---|
| 0 (指定用户) | 点击后固定消息回复 |
| 1 (点击按钮弹出输入框) | 触发用户输入 |
| 2 (点击按钮弹出自动回复) | 自动回复指定文案 |
| 3 (点击按钮触发交互) | 触发服务端回调 (WebSocket interaction) |
| 4 (点击按钮打开链接) | URL 跳转 |

**交互 Keyboard (#3)**:

```json
{
  "keyboard": {
    "content": {
      "rows": [{
        "buttons": [{
          "id": "btn1",
          "render_data": { "label": "确认", "style": 1 },
          "action": {
            "type": 2,   // 2=回调
            "permission": { "type": 0 },
            "data": "cmd:/confirm",
            "reply": true
          }
        }]
      }]
    }
  }
}
```

按钮风格 (style): 1=蓝, 2=灰, 3=绿, 4=红

#### Markdown

QQ Bot Markdown 支持 **Markdown 子集**：
- 标题 (# → ######)
- 粗体/斜体/删除线
- 有序/无序列表
- 代码块
- 引用
- 链接
- 分割线

**不支持**: HTML 标签、图片嵌入、表格

#### 当前实现与能力差距

| 能力 | 实现状态 |
|---|---|
| text/markdown | ✅ (`markdown_support=true`) |
| Keyboard (交互类型 3) | ❌ 可用于按钮交互 |
| Keyboard (链接类型 4) | ❌ 可用于 URL 分享 |
| ARK 消息 | ❌ 结构化卡片 |
| 交互回调处理 | ❌ 通过 WebSocket interaction 事件 |

### 建议实现

```
优先级 1: Keyboard 消息 (交互类型 3) → InlineButtonSender
优先级 2: ARK 消息 → 基础卡片 (仅展示)
优先级 3: 交互回调 → 通过 WebSocket Gateway
```

---

## 8. LINE

### 当前实现状态: ★☆☆☆☆ 纯文本 (stripped markdown)

**已实现接口**: `ReplyContextReconstructor`, `SendImage` (有方法但无显式接口断言)

**未实现**: 全部卡片和交互能力

### 原生 API 能力

#### 消息类型

| 类型 | 说明 |
|---|---|
| Text | 纯文本 + emoji |
| Text (v2) | 支持更多格式化 |
| Sticker | 贴纸 |
| Image | 图片 |
| Video | 视频 |
| Audio | 音频 |
| Location | 位置 |
| Imagemap | 图片热区映射（可点击区域） |
| Flex Message | **自定义布局卡片** |
| Template Message | 预设模板卡片 |
| Coupon | 优惠券 |

#### Flex Message — LINE 的核心卡片系统

**容器类型**:

| 容器 | 说明 |
|---|---|
| Bubble | 单卡片容器 (header/hero/body/footer) |
| Carousel | 多卡片轮播 (最多 12 个 Bubble) |

**Bubble 布局**:

```
┌──────────────────┐
│ Header            │ ← header (标题)
├──────────────────┤
│ Hero              │ ← hero (大图)
├──────────────────┤
│ Body              │ ← body (内容)
├──────────────────┤
│ Footer            │ ← footer (按钮)
└──────────────────┘
```

**组件类型 (Flex Component)**:

| 组件 | 说明 |
|---|---|
| Box | 布局容器 (horizontal/vertical/baseline) |
| Text | 文本 (支持样式/换行/margin等) |
| Image | 图片 (支持 aspect_ratio/fit) |
| Button | 按钮 (高度固定, 支持 action) |
| Icon | 图标 |
| Separator | 分隔线 |
| Span | 内联文本片段 (用于富文本) |
| Filler | 弹性占位符 |
| Spacer | 固定间距 |

**Action 类型**:

| Action | 说明 |
|---|---|
| Postback | 回传数据到 bot (最常用于交互) |
| Message | 发送指定文本 (模拟用户消息) |
| URI | 打开 URL |
| Datetimepicker | 日期时间选择器 |
| Camera / CameraRoll | 拍照/选图 |
| Location | 位置选择器 |
| Rich Menu Switch | 切换菜单标签 |
| Clipboard | 复制到剪贴板 |

**限制**:
- Bubble: 单个 Bubble 内容 ≤ 10KB JSON
- Carousel: 最多 12 个 Bubble
- Postback data: 最大 300 字节

#### Template Messages (预设模板)

| 模板 | 说明 |
|---|---|
| Buttons | 标题 + 文本 + 最多 4 个按钮 |
| Confirm | 确认对话框 (2 按钮) |
| Carousel | 多列轮播 (最多 10 列) |
| Image Carousel | 图片轮播 (最多 10 张) |

#### 当前实现与能力差距

| 能力 | 实现状态 |
|---|---|
| Text (strip markdown) | ✅ |
| Flex Message (Bubble) | ❌ 完整卡片系统未接入 |
| Flex Message (Carousel) | ❌ |
| Template (Buttons/Confirm) | ❌ 简单交互模板 |
| Postback Action | ❌ 回调处理 |
| Datetimepicker Action | ❌ |
| Rich Menu | ❌ 持久化菜单 |

### 建议实现

```
优先级 1: Template (Buttons/Confirm) → 简单交互
优先级 2: Flex Message (Bubble) → CardSender 映射
优先级 3: Flex Message (Carousel) → 多项浏览
优先级 4: Datetimepicker → Cron 时间配置
```

---

## 9. MAX

### 当前实现状态: ★★☆☆☆ 按钮 + 附件管理

**已实现接口**: `InlineButtonSender`, `ImageSender`, `FileSender`, `AudioSender`, `MessageUpdater`, `TypingIndicator`, `FormattingInstructionProvider`

**未实现**: `CardSender`, 卡片系统

### 原生 API 能力 (基于代码分析)

MAX 是一个较新的 IM 平台，通过自定义 API 接入。

**消息格式**:
```
支持 Markdown 子集: **bold**, _italic_, inline code, fenced code blocks, bullet lists
不支持: headers, horizontal rules, tables, HTML
换行: 需要 markdown hard breaks (两个尾随空格 + \n)
```

**Inline Keyboard**:
```json
{
  "type": "inline_keyboard",
  "payload": {
    "buttons": [
      [{ "type": "callback", "text": "确认", "payload": "cmd:/confirm" }],
      [{ "type": "callback", "text": "取消", "payload": "cmd:/cancel" }]
    ]
  }
}
```
- 按钮类型: `callback` (触发 `message_callback` 事件)
- 回调处理: 解析 `message_callback` update、派发 payload 作为消息内容

**附件管理**:
- CDN 两步上传: 先上传到 CDN，获取 attachment ID，再通过 message API 引用
- 重试机制: 处理 "attachment.not.ready" 错误

### 建议实现

```
优先级 1: 完善 InlineKeyboard 回调处理
优先级 2: 探索原生卡片 API (如有)
```

---

## 10. Shadow OB

### 当前实现状态: ★★★☆☆ 按钮 + 交互块 + 预览

**已实现接口**: `InlineButtonSender`, `ImageSender`, `FileSender`, `MessageUpdater`, `PreviewStarter`, `PreviewCleaner`, `TypingIndicator`, `ProgressStyleProvider`, `CommandRegistrar`, `FormattingInstructionProvider`

**未实现**: `CardSender`

### 能力概述 (基于代码分析)

Shadow OB 通过 Socket.io 连接，有自定义的交互块系统。

**Interactive Blocks**:
```json
{
  "kind": "buttons",
  "buttons": [
    { "id": "btn_1", "label": "确认", "value": "cmd:/confirm" },
    { "id": "btn_2", "label": "取消", "value": "cmd:/cancel" }
  ]
}
```
- 通过 `ccConnectDelivery` metadata 结构传递
- 响应解析自 `metadata.interactiveResponse`
- 支持 `cmd:`, `askq:`, `perm:` 回调值前缀

**Progress 风格**: 支持 `"compact"` 和 `"card"` 两种配置

**Formatting**:
```
支持标准 Markdown: **bold**, *italic*, code blocks, tables (under 8 columns, 30 rows)
```

**架构特色**:
- 专为 cc-connect 设计的自定义协议
- Socket.io 实时连接
- 交互式斜杠命令表单
- `UPSTREAM.md` 记录了与 core engine 的交互问题

### 建议实现

```
优先级 1: 统一交互块 API 到 core.Card 映射
优先级 2: 丰富按钮样式支持
```

---

## 11. 其他平台

### Weixin (个人微信)

**状态**: ★☆☆☆☆ 纯文本

**已实现**: `ImageSender`, `FileSender`, `TypingIndicator`, `FormattingInstructionProvider`

**限制**: 个人微信无官方 Bot API，使用第三方通道。纯文本交付。CDN 直传（绕过代理）用于图片/文件。

### QQ (OneBot v11)

**状态**: ★☆☆☆☆ 纯文本

**已实现**: `ImageSender`, `ReplyContextReconstructor`

**限制**: OneBot v11 协议。无卡片/按钮能力。CQ 码处理 (`stripCQCodes()`)。

### Weibo (微博)

**状态**: ★☆☆☆☆ 纯文本 + 文件

**已实现**: `ImageSender`, `FileSender`

**限制**: 自定义 WebSocket 协议。无交互能力。Base64 编码图片/文件传输。

---

## 能力矩阵总览

### 交互能力对比

| 平台 | 结构化卡片 | 内联按钮 | 选择器 | 表单 | 弹窗 Modal | 卡片原地更新 | 进度卡片 |
|------|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| **Feishu** | ✅ 消息卡片 v1 | ✅ (卡片内) | ✅ select_static | ✅ form+checker | ❌ | ✅ Patch API | ✅ RichCard |
| **Discord** | ✅ Embed / Components v2 | ✅ Button (v1/v2) | ✅ 5种Select | ✅ (Modal) | ✅ Modal | ❌ | ✅ Embed |
| **Slack** | ✅ Block Kit (18 Blocks) | ✅ 11种组件 | ✅ 多种Select | ✅ Input + Modal | ✅ Modal | ✅ replace | — |
| **Telegram** | ❌ (无原生卡片) | ✅ InlineKeyboard | ❌ | ❌ | ✅ Web App | ❌ | — |
| **DingTalk** | ✅ 互动卡片 | ✅ (卡片内) | ✅ (卡片内) | ✅ (卡片内) | ✅ Workbench | ✅ | ✅ AI流式卡 |
| **WeCom** | ✅ template_card (5种) | ✅ button_interaction | ✅ select_list | ✅ vote/multiple | ❌ | ✅ response_code | — |
| **QQ Bot** | ✅ ARK | ✅ Keyboard (type 3) | ❌ | ❌ | ❌ | ❌ | — |
| **LINE** | ✅ Flex Message | ✅ (Footer) | ❌ | ❌ | ❌ | ❌ | — |
| **MAX** | ❌ | ✅ InlineKeyboard | ❌ | ❌ | ❌ | ❌ | — |
| **Shadow OB** | ❌ | ✅ Interactive Block | ❌ | ❌ | ❌ | ❌ | — |
| **Weixin** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | — |
| **QQ** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | — |
| **Weibo** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | — |

### 当前 cc-connect 实现状态

| 平台 | CardSender | CardNavigable | CardRefresher | InlineBtnSender | RichCard | Preview | 评级 |
|------|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Feishu | ✅ | ✅ | ✅ | — | ✅ | ✅ | ★★★★★ |
| Discord | — | — | — | ✅ | —* | ✅ | ★★★☆☆ |
| Slack | — | — | — | — | — | — | ★☆☆☆☆ |
| Telegram | — | — | — | ✅ | — | ✅ | ★★★☆☆ |
| DingTalk | — | — | — | — | — | — | ★☆☆☆☆ |
| WeCom | — | — | — | — | — | — | ★☆☆☆☆ |
| QQ Bot | — | — | — | — | — | — | ★☆☆☆☆ |
| LINE | — | — | — | — | — | — | ★☆☆☆☆ |
| MAX | — | — | — | ✅ | — | — | ★★☆☆☆ |
| Shadow OB | — | — | — | ✅ | — | ✅ | ★★★☆☆ |

> \* Discord 有 progress Embed (progress.go)，但不是通过 RichCardSupporter 接口

### 消息长度限制

| 平台 | 文本限制 | 备注 |
|------|---------|------|
| Feishu | 30KB (JSON) | 卡片 JSON 总大小 |
| Discord | 2000 字符 (content) / 6000 字符 (embed description) / 40 组件 | Components v2 |
| Slack | 3000 字符 (text) / 50 blocks | Block Kit |
| Telegram | 4096 字符 | 文本消息 |
| DingTalk | 20480 字节 | Markdown 消息 |
| WeCom | 2048 字节 (markdown) / 无明确限制 (template_card) | — |
| QQ Bot | 2000 字符 (text) / 5000 字符 (markdown) | 群/C2C |
| LINE | 5000 字符 | 文本消息 |
| MAX | 4000 字符 | — |
| Weibo | 2000 字符 | — |
| Weixin | 2048 字节 | — |

---

## 实现建议

### 短期 (高优先级)

1. **Slack Block Kit** — 投入产出比最高
   - 映射 `core.Card` → Block Kit (Card → Section + Actions)
   - 约 300-500 行代码
   - Slack 用户量大，反馈最强烈

2. **WeCom template_card** — 亚洲市场覆盖
   - 实现 `button_interaction` 子类型
   - 约 400 行代码
   - 覆盖企业微信企业用户

3. **DingTalk ActionCard** — 轻量级交互
   - 独立按钮型 ActionCard（URL 跳转）
   - 约 150 行代码

### 中期

4. **LINE Flex Message** — 日本市场
   - Bubble 容器映射 `core.Card`
   - 约 400 行代码

5. **Discord Components v2** — 升级现有按钮
   - Section + String Select
   - 约 300 行代码

6. **QQ Bot Keyboard** — 
   - Keyboard type 3 (交互回调)
   - 约 250 行代码

### 长期

7. **Discord Modal** — 表单交互
8. **Telegram Web App** — 配置面板
9. **DingTalk AI 流式卡片** — 进度展示
10. **Feishu 消息卡片 v2** — 升级卡片系统

### 架构建议

当前 `core.Card` 抽象设计良好，但可考虑扩展：

```go
// 建议新增的 CardElement
type CardImage struct { URL, AltText string }           // 卡片内图片
type CardList struct { Items []CardListItem }            // 多行列表
type CardPicker struct { Type string; ... }              // 日期/时间选择器 (LINE)
type CardModal struct { Title string; Blocks []any; ... } // 弹窗表单 (Discord/Slack)

// 建议新增的能力接口
type ModalOpener interface {                             // 弹窗能力
    OpenModal(ctx, replyCtx, *Modal) (*ModalResult, error)
}
type MessagePinner interface {                           // 消息置顶
    PinMessage(ctx, replyCtx) error
}
```
