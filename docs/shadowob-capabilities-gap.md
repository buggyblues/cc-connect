# Shadow OB 平台能力差距分析与实现路线

> 对比基准: Discord 平台实现 (`platform/discord/`)
> 分析日期: 2026-05-15

## 1. 当前能力对比

### 已实现接口

| 接口 | Discord | Shadow OB | 说明 |
|------|:--:|:--:|------|
| `InlineButtonSender` | ✅ | ✅ | 内联按钮 |
| `ImageSender` | ✅ | ✅ | 图片发送 |
| `FileSender` | ✅ | ✅ | 文件发送 |
| `MessageUpdater` | ✅ | ✅ | 消息编辑 |
| `PreviewStarter` | ✅ | ✅ | 流式预览启动 |
| `PreviewCleaner` | ✅ | ✅ | 预览消息清除 |
| `TypingIndicator` | ✅ | ✅ | 输入状态指示 |
| `CommandRegistrar` | ✅ | ✅ | 命令注册 |
| `AsyncRecoverablePlatform` | — | ✅ | Shadow 独有的异步重连 |

### 缺失接口 (Discord 有，Shadow OB 无)

| 接口 | Discord | Shadow OB | 重要性 |
|------|:--:|:--:|:--:|
| **`CardSender`** | — | — | ★★★★ |
| **`ProgressCardPayloadSupport`** | ✅ | — | ★★★★★ |
| **`ProgressUpdateThrottler`** | ✅ | — | ★★★ |
| **`PreviewFinishPreference`** | ✅ | — | ★★ |
| **`ChannelNameResolver`** | ✅ (没有显式接口但实现) | — | ★★ |
| **`MarkdownTableSplitter`** | 间接 (format.go) | — | ★★ |

---

## 2. 详细能力差距

### 2.1 缺少 CardSender — 结构化卡片

**现状**: Shadow OB 只能发送纯文本 + 内联按钮。当 engine 产生 `core.Card` 时，Shadow OB 走 `InlineButtonSender` 降级路径：文本用 `card.RenderText()` 渲染，按钮用 `card.CollectButtons()` 提取。

**丢失的内容**:
- `CardHeader` → 彩色标题栏 → 降级为 `**title**\n\n` 纯文本
- `CardMarkdown` → 结构化 Markdown → 保持但无卡片容器
- `CardDivider` → 视觉分隔线 → 降级为 `---`
- `CardNote` → 底部脚注 → 降级为普通文本
- `CardListItem` → 文本 + 右侧按钮 → 降级为 `text [btn]`
- `CardSelect` → 下拉选择器 → 降级为 `选择: opt1 | opt2`

**Discord 的做法**:
Discord 同样没有实现 `CardSender`（也只走 `InlineButtonSender`）。但 Discord 通过 `ProgressCardPayloadSupport` 在进度场景下用 Embed 实现了结构化视觉呈现。

**Shadow OB 应如何实现**:

Shadow OB 已有 `shadowInteractiveBlock` 数据结构，支持 4 种交互类型：

```
shadowInteractiveBlock
├── kind: "buttons"     → 按钮组 (当前 SendWithButtons 使用)
├── kind: "select"      → 下拉选择器
├── kind: "form"        → 表单 (text/textarea/number/checkbox/select 字段)
└── kind: "approval"    → 审批流
```

需要新增一个 `CardSender` 实现，将 `core.Card` 映射到 Shadow 的消息格式。有两种策略：

**策略 A: 复用现有 metadata 通道 (推荐)**

将 Card 序列化嵌入 `ccConnectDelivery` metadata，由 Shadow 客户端根据 `kind: "card"` 渲染：

```json
{
  "content": "Card 文本降级内容 (兼容不支持客户端的兜底)",
  "metadata": {
    "ccConnectDelivery": {
      "id": "...",
      "source": "cc-connect-shadowob"
    },
    "interactive": {
      "id": "card_xxx",
      "kind": "card",
      "header": { "title": "Session Info", "color": "blue" },
      "elements": [
        { "kind": "markdown", "content": "..." },
        { "kind": "divider" },
        { "kind": "buttons", "buttons": [...] },
        { "kind": "select", "options": [...] },
        { "kind": "note", "text": "..." }
      ]
    }
  }
}
```

**策略 B: 扩展 `shadowInteractiveBlock` 增加 `kind: "card"` 和 `kind: "list_item"`**

在现有的 `shadowInteractiveBlock` 中增加新的 kind 值和对应的字段：

```go
type shadowInteractiveBlock struct {
    // 现有字段
    ID      string                  `json:"id"`
    Kind    string                  `json:"kind"`
    Prompt  string                  `json:"prompt,omitempty"`
    Buttons []shadowInteractiveItem `json:"buttons,omitempty"`
    Options []shadowInteractiveItem `json:"options,omitempty"`
    Fields  []shadowInteractiveField `json:"fields,omitempty"`
    // ...

    // 新增: Card 相关
    Header   *shadowCardHeader   `json:"header,omitempty"`
    Elements []shadowCardElement `json:"elements,omitempty"`
}

type shadowCardHeader struct {
    Title string `json:"title"`
    Color string `json:"color"`
}

type shadowCardElement struct {
    Kind    string `json:"kind"`    // "markdown", "divider", "note", "list_item"
    Content string `json:"content,omitempty"`
    Item    *shadowCardListItem `json:"item,omitempty"`
}

type shadowCardListItem struct {
    Text    string `json:"text"`
    BtnText string `json:"btnText"`
    BtnType string `json:"btnType"`
    BtnValue string `json:"btnValue"`
}
```

### 2.2 缺少 ProgressCardPayloadSupport — 进度卡片

**现状**: Shadow OB 的 `ProgressStyle()` 返回 `"compact"` (可配置)。compact 模式下，进度展示为纯文本行：
```
🔧 Bash: npm install
✅ Bash: npm test (passed)
```

**Discord 的做法**:

`ProgressCardPayloadSupport` 接口：
```go
type ProgressCardPayloadSupport interface {
    SupportsProgressCardPayload() bool
}
```

当返回 `true` 时，engine 将 `ProgressCardPayload` 嵌入消息内容，Discord 的 `buildDiscordPreviewMessage()` 解析该 payload 并渲染为 **Discord Embed**：

```
┌─────────────────────────────────────┐
│ Claude · Processing                 │ ← blue title bar
├─────────────────────────────────────┤
│ 💭 Analyzing requirements...        │
│ 🔧 Bash — npm install               │
│ 🧾 Bash — ok · exit 0              │
│ ❌ Bash — npm test failed          │
│ ℹ️ Showing latest updates only.     │
├─────────────────────────────────────┤
│ Full response is in the next msg    │ ← footer
└─────────────────────────────────────┘
```

**Shadow OB 应如何实现**:

Shadow OB 没有原生 Embed，但在 metadata 通道中可以携带同样的结构化数据。方案：

```
engine 生成 ProgressCardPayload
  → Shadow OB 检测到 ProgressCardPayload
  → 构建 shadowInteractiveBlock { kind: "progress_card" }
  → 嵌入 metadata.interactive
  → Shadow 客户端渲染为进度卡片 UI（带颜色、图标、状态）
  → UpdateMessage 时更新同一卡片
```

需要实现的接口：

```go
// 声明实现
var _ core.ProgressCardPayloadSupport = (*Platform)(nil)
var _ core.ProgressUpdateThrottler = (*Platform)(nil)

func (p *Platform) SupportsProgressCardPayload() bool { return true }

func (p *Platform) ProgressUpdateInterval() time.Duration { return 1 * time.Second }
```

实现要点：

1. **`SendPreviewStart()`** 改造 — 检测 `ProgressCardPayload`，构建进度卡片 interactive block
2. **`UpdateMessage()`** 改造 — 检测 `ProgressCardPayload`，更新进度卡片而不是更新文本
3. **进度卡片 interactive block 结构**:

```json
{
  "id": "progress_xxx",
  "kind": "progress_card",
  "progress": {
    "state": "running",          // running | completed | failed
    "agentLabel": "Claude",
    "stateText": "Processing",   // 国际化
    "color": "#5865F2",         // blue=#5865F2, green=#57F287, red=#ED4245
    "items": [
      { "kind": "thinking", "text": "Analyzing..." },
      { "kind": "tool_use", "tool": "Bash", "summary": "npm install" },
      { "kind": "tool_result", "tool": "Bash", "summary": "ok", "exitCode": 0 },
      { "kind": "error", "text": "build failed" }
    ],
    "truncated": false,
    "footer": "Full response in next message"
  }
}
```

### 2.3 缺少 PreviewFinishPreference

**现状**: engine 在 `sp.finish()` 中，如果平台实现了 `PreviewCleaner` 且没有声明 `KeepPreviewOnFinish()`，会删除预览消息再发送最终消息。

**Discord**: 实现了 `KeepPreviewOnFinish() -> true`，让进度卡片保留在聊天中（带 Completed 状态和 footer），不删除。

**Shadow OB 应增加**:

```go
func (p *Platform) KeepPreviewOnFinish() bool { return true }
```

这样进度卡片在完成后保留，用户可以看到完整的过程记录。

### 2.4 缺少 ChannelNameResolver

**现状**: Shadow OB 内部通过 `resolveChannelName()` 获取频道名称，存在 `channelRuntime.Name` 中。但这个方法没有暴露为 `core.ChannelNameResolver` 接口。

**应增加**:

```go
var _ core.ChannelNameResolver = (*Platform)(nil)

func (p *Platform) ResolveChannelName(channelID string) (string, error) {
    p.mu.RLock()
    rt, ok := p.channels[channelID]
    p.mu.RUnlock()
    if ok && rt.Name != "" {
        return rt.Name, nil
    }
    // 如果需要实时查询
    p.resolveChannelName(context.Background(), channelID)
    p.mu.RLock()
    rt, _ = p.channels[channelID]
    p.mu.RUnlock()
    if rt.Name != "" {
        return rt.Name, nil
    }
    return "", core.ErrNotSupported
}
```

### 2.5 按钮样式映射不足

**现状**: `SendWithButtons()` 将所有按钮的 `Style` 字段留空，由 Shadow 客户端决定默认样式。

```go
// 当前实现 (shadowob.go:973-978)
block.Buttons = append(block.Buttons, shadowInteractiveItem{
    ID:    truncateString(button.Data, 80),
    Label: truncateString(button.Text, 120),
    Value: button.Data,
    // Style 字段未设置!
})
```

**Discord 的做法**: 自动按位置分配按钮颜色（0=绿, 1=红, 2=蓝紫, default=灰）。

**应修改为**:

从 `core.CardButton.Type` 映射到 `shadowInteractiveItem.Style`：

```go
func mapButtonStyle(btnType string) string {
    switch btnType {
    case "primary":
        return "primary"
    case "danger":
        return "destructive"
    case "default":
        return "secondary"
    default:
        return ""
    }
}

// 在 SendWithButtons 中
block.Buttons = append(block.Buttons, shadowInteractiveItem{
    ID:    truncateString(button.Data, 80),
    Label: truncateString(button.Text, 120),
    Value: button.Data,
    Style: mapButtonStyle(button.Type),  // 新增
})
```

### 2.6 Markdown 表格处理

**现状**: Shadow OB 的 `FormattingInstructions()` 警告用户 "Tables should be kept under 8 columns and 30 rows"，但没有做自动格式化处理。

**Discord 的做法**: `format.go` 中的 `wrapTablesInCodeBlocks()` 自动检测 Markdown 表格并包裹在 fenced code block 中（因为 Discord 原生不支持表格渲染）。

**Shadow OB 应检查** Shadow 客户端是否原生支持 Markdown 表格：
- 如果支持 → 不需要自动处理
- 如果不支持 → 需要类似的表格转义处理

### 2.7 交互系统整合

**现状**: Shadow OB 有两套互不通用的交互系统：

| 系统 | 触发路径 | 数据结构 |
|------|---------|---------|
| 运行时按钮 | `SendWithButtons()` | 手动构建 `shadowInteractiveBlock{Kind:"buttons"}` |
| 斜杠命令交互 | `handleLocalSlashPrompt()` | 从配置文件加载完整 `shadowInteractiveBlock` |

运行时按钮只能用 `kind: "buttons"`（无 select/form/approval），而斜杠命令支持全部 4 种 kind。

**建议**: 统一为运行时交互也提供完整的 Block 能力。暴露一个新的方法供 engine 或更上层的调用使用：

```go
// SendInteractiveBlock 发送任意交互块（运行时生成）
func (p *Platform) SendInteractiveBlock(ctx context.Context, replyCtx any, block shadowInteractiveBlock) error {
    rc, ok := replyCtx.(replyContext)
    if !ok {
        return fmt.Errorf("shadowob: invalid reply context")
    }
    content := firstNonEmpty(block.Prompt, "​")
    metadata := p.deliveryMetadata(map[string]any{"interactive": block})
    _, err := p.sendToReplyContext(ctx, rc, content, false, metadata)
    return err
}
```

---

## 3. 实现优先级路线

### Phase 1: 进度卡片 (1-2 天)

这是最有视觉冲击力的改进。

1. **`ProgressCardPayloadSupport`**
   - 实现 `SupportsProgressCardPayload() -> true`
   - 修改 `SendPreviewStart()` 检测 ProgressCardPayload
   - 构建 `kind: "progress_card"` interactive block
   - 修改 `UpdateMessage()` 支持进度卡片更新
   - 实现 `ProgressUpdateThrottler` (1s interval)

2. **`PreviewFinishPreference`**
   - 实现 `KeepPreviewOnFinish() -> true`

3. **按钮样式映射**
   - `mapButtonStyle()` 函数
   - 修改 `SendWithButtons()` 传递 style

### Phase 2: 结构化卡片 (2-3 天)

4. **`CardSender` 实现**
   - 定义 Shadow Card 交互块格式 (`kind: "card"`)
   - 实现 `SendCard()` / `ReplyCard()`
   - 映射 `core.Card` → Shadow Card Block:
     - `CardHeader` → `header` 字段
     - `CardMarkdown` → `elements[].kind: "markdown"`
     - `CardDivider` → `elements[].kind: "divider"`
     - `CardActions` → `elements[].kind: "buttons"`
     - `CardListItem` → `elements[].kind: "list_item"`
     - `CardSelect` → `elements[].kind: "select"`
     - `CardNote` → `elements[].kind: "note"`

5. **`CardNavigable` + `CardRefresher`**
   - 注册导航处理器 `SetCardNavigationHandler()`
   - 在 `interactiveResponseContent()` 中路由卡片交互回调
   - 实现 `RefreshCard()` 通过 `editMessage` 更新卡片

### Phase 3: 平台完善 (1-2 天)

6. **`ChannelNameResolver`**
   - 暴露 `ResolveChannelName()`

7. **交互系统统一**
   - `SendInteractiveBlock()` 方法
   - `SendWithButtons()` 内部调用 `SendInteractiveBlock()`
   - 支持运行时 select/form block

8. **Markdown 表格处理** (按需)
   - 确认 Shadow 客户端表格支持情况
   - 若需要则实现 table wrapper

---

## 4. 新增接口和类型总览

### 需要新增的接口实现

```go
// 编译时检查
var _ core.CardSender                    = (*Platform)(nil)
var _ core.CardNavigable                 = (*Platform)(nil)
var _ core.CardRefresher                 = (*Platform)(nil)
var _ core.ProgressCardPayloadSupport    = (*Platform)(nil)
var _ core.ProgressUpdateThrottler       = (*Platform)(nil)
var _ core.PreviewFinishPreference       = (*Platform)(nil)
var _ core.ChannelNameResolver           = (*Platform)(nil)
// var _ core.MarkdownTableSplitter       = (*Platform)(nil)  // 按需
```

### 需要新增的方法

| 方法 | 来源接口 | 说明 |
|------|---------|------|
| `SendCard(ctx, replyCtx, *Card) error` | CardSender | 发送新卡片消息 |
| `ReplyCard(ctx, replyCtx, *Card) error` | CardSender | 回复卡片消息 |
| `SetCardNavigationHandler(h CardNavigationHandler)` | CardNavigable | 设置卡片交互回调 |
| `RefreshCard(ctx, sessionKey, *Card) error` | CardRefresher | 更新已发送的卡片 |
| `SupportsProgressCardPayload() bool` | ProgressCardPayloadSupport | 支持进度卡片 payload |
| `ProgressUpdateInterval() time.Duration` | ProgressUpdateThrottler | 进度更新节流间隔 |
| `KeepPreviewOnFinish() bool` | PreviewFinishPreference | 完成后保留预览 |
| `ResolveChannelName(channelID string) (string, error)` | ChannelNameResolver | 频道名解析 |

### 需要新增的数据结构

```go
// shadowCardHeader 卡片标题栏
type shadowCardHeader struct {
    Title string `json:"title"`
    Color string `json:"color"` // blue/green/red/orange/purple/grey
}

// shadowCardElement 卡片内容元素
type shadowCardElement struct {
    Kind    string              `json:"kind"`
    Content string              `json:"content,omitempty"`
    Item    *shadowCardListItem `json:"item,omitempty"`
}

// shadowCardListItem 列表项
type shadowCardListItem struct {
    Text    string `json:"text"`
    BtnText string `json:"btnText"`
    BtnType string `json:"btnType"`
    BtnValue string `json:"btnValue"`
}

// shadowProgressBlock 进度卡片的载荷
type shadowProgressBlock struct {
    State       string                  `json:"state"`       // running/completed/failed
    AgentLabel  string                  `json:"agentLabel"`
    StateText   string                  `json:"stateText"`   // 国际化状态文本
    Color       string                  `json:"color"`       // hex
    Items       []shadowProgressItem    `json:"items"`
    Truncated   bool                    `json:"truncated"`
    Footer      string                  `json:"footer,omitempty"`
}

// shadowProgressItem 进度卡片单行
type shadowProgressItem struct {
    Kind     string `json:"kind"`     // thinking/tool_use/tool_result/error/info
    Tool     string `json:"tool,omitempty"`
    Text     string `json:"text"`
    Status   string `json:"status,omitempty"`
    ExitCode *int   `json:"exitCode,omitempty"`
}
```

### 需要修改的方法

| 方法 | 变更说明 |
|------|---------|
| `SendWithButtons()` | 增加按钮 style 映射 |
| `SendPreviewStart()` | 检测 ProgressCardPayload，构建进度交互块 |
| `UpdateMessage()` | 支持进度卡片更新（不只是文本） |
| `sendToReplyContext()` | 重构以复用交互块构造逻辑 |

### 需要新增的辅助函数

| 函数 | 说明 |
|------|------|
| `renderCardBlock(card *core.Card) *shadowInteractiveBlock` | core.Card → Shadow card block |
| `renderProgressBlock(payload *core.ProgressCardPayload) *shadowInteractiveBlock` | ProgressCardPayload → Shadow progress block |
| `sendInteractiveBlock(ctx, rc, block) (*shadowMessage, error)` | 统一交互块发送 |
| `mapButtonStyle(btnType string) string` | 按钮类型 → style 映射 |
| `buildCardInteractiveBlockID(card *core.Card) string` | 生成唯一 card block ID |

---

## 5. 交互流程对比

### 当前流程 (InlineButtonSender)

```
Engine 产生 Card (有按钮)
  → card.HasButtons() = true
  → p.(InlineButtonSender).SendWithButtons(content, buttons)
  → Shadow OB: 构建 kind:"buttons" block
  → sendToReplyContext() → POST /api/channels/{id}/messages
  → 用户点击按钮
  → Shadow 服务端 POST interactive_response 到 metadata
  → WebSocket 收到 message:new (含 interactiveResponse)
  → interactiveResponseContent() 解析为文本
  → dispatch 到 engine 作为新消息
```

### 期望流程 (CardSender + CardNavigable)

```
Engine 产生 Card
  → p.(CardSender).ReplyCard(card)
  → Shadow OB: renderCardBlock(card) → kind:"card" block
  → sendToReplyContext() → POST /api/channels/{id}/messages
  → 用户点击按钮/选择选项/提交表单
  → WebSocket 收到 message:new (含 interactiveResponse)
  → 提取 action + session_key
  → CardNavigationHandler(action, sessionKey)
  → 返回新 Card
  → 构造新 kind:"card" block
  → 回复新卡片消息
  → 或 RefreshCard(sessionKey, newCard)
  → PATCH /api/messages/{message_id}
```

---

## 6. 工作量估算

| Phase | 内容 | 工作量 | 新增代码 |
|-------|------|:--:|:--:|
| 1 | 进度卡片 + 完成保留 + 按钮样式 | 1-2 天 | ~250 行 |
| 2 | CardSender + CardNavigable + CardRefresher | 2-3 天 | ~400 行 |
| 3 | ChannelName + 交互统一 + 表格 | 1-2 天 | ~150 行 |
| 测试 | 各接口单元测试 | 1-2 天 | ~300 行 |
| **合计** | | **5-9 天** | **~1100 行** |

---

## 7. 与 Discord 实现的终极对比

| 能力维度 | Discord 当前 | Shadow OB 当前 | Shadow OB 目标 |
|---------|:--:|:--:|:--:|
| 结构化卡片渲染 | — (无 CardSender) | — | ✅ CardSender |
| 进度卡片 (Embed/Block) | ✅ ProgressCardPayloadSupport | — | ✅ ProgressCardPayloadSupport |
| 卡片交互回调 | — (无 CardNavigable) | — | ✅ CardNavigable |
| 卡片原地刷新 | — | — | ✅ CardRefresher |
| 内联按钮 | ✅ (仅 slash 交互) | ✅ | ✅ |
| 下拉选择器 | — | ✅ (仅 slash 命令) | ✅ (运行时) |
| 表单输入 | — (Discord Modal) | ✅ (仅 slash 命令) | ✅ (运行时) |
| 审批流 | — | ✅ (仅 slash 命令) | — (保持 slash 命令) |
| 预览消息 | ✅ | ✅ | ✅ |
| 预览完成保留 | ✅ | — | ✅ |
| 输入状态指示 | ✅ | ✅ | ✅ |
| 频道名解析 | ✅ | — | ✅ |
| 按钮样式 | ✅ (自动映射) | — | ✅ (手动映射) |
| 节流更新 | ✅ (2s) | — | ✅ (1s) |
| 国际化进度文本 | ✅ (5 语言) | — | ✅ (5 语言) |

完成全部实现后，Shadow OB 将成为 cc-connect 中 **仅次于 Feishu 的第二完整平台实现**。
