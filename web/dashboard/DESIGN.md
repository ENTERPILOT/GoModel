# DavAi Gateway Dashboard 设计规范（对齐 Arco Design）

> **状态**：P0 已实施并通过校验（`npm run check` / `npm test` / `npm run build`）
> **适用范围**：`web/dashboard/`（Svelte 5 + 原生 CSS 管理后台）
> **配套约定**：实现层面的规则见 [`CONVENTIONS.md`](./CONVENTIONS.md)。本文是**视觉规范的事实源**，CONVENTIONS.md 是**编码约定的事实源**，两者职责不重叠。

---

## 1. 背景与目标

当前 dashboard 自建了一套暖色（棕 / 米色）token，缺少语义分层，状态色在浅色主题下不可读，部分控件尺寸与层级不符合企业级控制台惯例。

本次改造的目标：

1. 用 **Arco Design 官方色板与组件规格**替换现有自建色板；
2. 建立**两层 token 体系**（色阶 + 语义别名），让色板可扩展、语义可寻址；
3. 修掉现有 token 中的**对比度缺陷**（见 §3）；
4. 保留现有组件结构与 API 契约，不做页面重排。

**不在本次范围**：页面骨架重构、图表按 Arco `data-*` 色阶重做、导航分组、弹层结构。

---

## 2. 已确认的设计决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | **全量采用 Arco 官方色板**（primary `#165DFF`） | 品牌正从 GoModel 更名为 DavAi Gateway，暖色不再是必须延续的约束 |
| D2 | P0 **只换颜色值**，圆角 / 间距 / 字号留到 P1–P4 | `--radius` 被 60+ 处消费，与控件规格同批改更省事 |
| D3 | **两层 token**：底层 Arco 色阶，上层保留现有扁平名做别名 | 201 处 `var(--accent)` 引用零变更，同时色阶齐全可支撑后续需求 |
| D4 | 主题切换由 JS **归一化写 `data-theme`**，删除 `prefers-color-scheme` 重复块 | 消除"改一块漏一块"的结构性隐患（缺陷 #8 即由此产生） |
| D5 | "受限"语义改用**中性灰**，不再占用 accent | 受限不是错误状态，不该与"主操作"同色 |
| D6 | 深色主按钮用**深色字**（`#17171A`） | 白字仅 3.74:1，深色字 4.79:1 |
| D7 | 加入 **token 对比度守卫测试** | 本次审计出的对比度缺陷全在人眼阈值之下 |
| D8 | 色阶落 **6 族**：primary / success / warning / danger / neutral / cyan | `data-20` 色阶在 P0 无消费方，等图表重做时再引入 |
| D9 | 图表 token 吞吐系列**保留暖棕** | 其编码是"冷暖 + 深浅 = 成本"，换掉即丢失唯一可读性 |
| D10 | ~~`--info` 合并为 `--primary` 别名~~ **已撤销**，见 §3.5 | 调查发现 `--info` 承载 5 类互不相干的语义，其中 2 类是**分类编码** |
| D17 | **`--info` 拆分并删除**（Q19）：分类编码改用新 token `--category-1`（Arco cyan） | 把"主题色"与"分类编码"彻底分开；cyan 与 primary 蓝有区分度，且落在 Arco 色板内 |
| D18 | **`chartTheme.js` 的 `cssVar()` 不改**（Q20）：6 个 `--chart-*` token 一律写字面量色值 | `resolveCssColor()` 每次调用都创建 DOM 节点，引进热路径不值得；字面量约束可测试、零运行时成本 |
| D19 | **定义 `--accent-strong`**（Q21） | 它表达的是真实需求（浅底上需要比主色更深的强调文字）；定义比删除便宜，8 处引用无需改动 |
| D20 | **P0 按完整清单实施**（Q22） | 其中 3 项是现存 bug 或对比度缺陷，留到 P1 等于让已知问题再活一轮 |
| D11 | P0 **只替换对比度不达标处**的手写 `color-mix` 配方 | 守"最小改动"原则，顺手清理留给 P1/P2 |
| D12 | system 模式**继续实时跟随**系统主题变化 | 保持现有能力，不因重构而回退 |
| D13 | 文档落在本文件；token 约定补进 `CONVENTIONS.md` | 单一事实源，避免两份规范漂移 |
| D14 | 浅色 muted 取 `#646E7D` | Arco 的 gray-7 在页面底 / 行悬停底上不达 AA（4.29 / 4.11） |
| D15 | 输入框边框沿用 `#C9CDD4`，不追求 3:1 | 与 Arco 官方一致；1.4.11 偏差显式记录（见 §7） |
| D16 | 不保留 legacy 暖色主题开关 | 品牌换代期引入双主题只会变成长期负担 |
| **D21** | **`--primary`（填充）与 `--primary-text`（文字）分层**；`--accent` 指向 `--primary-text` | 201 处 `--accent` 里绝大多数是当**文字/图标色**用的。深色主题的 primary-6 `#3C7EFF` 作文字只有 4.20:1，低于 AA；primary-7 `#689FFF` 有 5.98:1。填充场景改指 `--primary`（13 处） |
| **D22** | **浅色主题的状态色直接取文字安全级**（success-8 / warning-8 / danger-7），不再用 level-6 | Arco 的 level-6 是"填充色"：green-6 `#00B42A` 在白底仅 2.78:1。全站有 60+ 处 `color: var(--success｜warning｜danger)` 的状态文字。与其逐处改消费者，不如让 token 本身就是安全值 |
| **D23** | **新增 4 个角色 token**：`--on-fill`（饱和底上的文字）、`--meter-label`（仪表条上的百分比）、`--switch-thumb`（开关旋钮）、`--on-dark-fill`（预算周期深色底上的文字） | 这四类"文字压在色块上"的位置无法用单一对比色 token 表达：同一条仪表条在不同主题下切片明暗相反 |
| **D24** | **`--info` 删除，`--accent-hover` 删除** | 全部消费者已迁移；留着就是下一份会漂移的"第二个事实源" |
| **D25** | **缓存仪表条的"未命中"色与图表系列色解耦** | 图表线要 ≥3:1 于深色画布（要亮），仪表条要能压住白字（要暗），两者无法同值 |

---

## 3. 现状审计

### 3.1 对比度缺陷（实测）

改造前实测到的缺陷：

| # | 问题 | 实测 | 位置 |
|---|---|---|---|
| 1 | 深色主题主按钮白字 | **2.78:1**（AA 需 4.5） | `src/styles/buttons.css:29-34` |
| 2 | 浅色主题 success 绿在白底 | **1.92:1** | `src/styles/themes.css:90` + `src/styles/page-globals.css:54-58` |
| 3 | 浅色主题 warning 橙在米底 | **2.81:1** | `src/styles/themes.css:92` + `src/styles/alerts.css:12` |
| 4 | 浅色主题 muted 文本在页面底 | **4.27:1** | `src/styles/themes.css:86` |

**换 Arco 色板会新引入的缺陷**（实施时发现，已一并修掉）：

| # | 问题 | 实测 | 处置 |
|---|---|---|---|
| 5 | 浅色主题若取 Arco level-6 作状态**文字**（green-6 `#00B42A` / orange-6 `#FF7D00` / red-6 `#F53F3F`） | **2.78 / 2.57 / 3.71:1** | D22：浅色主题直接取 level-8 / level-8 / level-7 |
| 6 | 深色主题 primary-6 `#3C7EFF` 作文字色（201 处 `--accent` 中绝大多数是文字） | **4.20:1**（surface 上） | D21：文字改用 primary-7 `#689FFF` |
| 7 | 深色主题主按钮白字 | **3.74:1** | D6：改用 `--on-fill`（`#17171A`，4.79:1） |
| 8 | 深色主题 danger 底上的白字（`#F76965`） | **2.93:1** | 改用 `--on-fill`（6.10:1） |

### 3.2 结构与一致性问题

| # | 问题 | 证据 |
|---|---|---|
| 9 | 棕色主按钮挂着**硬编码蓝色辉光** `rgba(59,130,246,.18)` | `src/styles/buttons.css:33` |
| 10 | `--accent` 同时充当品牌色 / 状态色 / 焦点色 | **201 处 `var(--accent)`**（消费侧 197，跨 51 个文件）；其中 14 处直接作背景色，111 处经 `color-mix` 派生边框与轮廓 |
| 11 | ~~表头与徽标使用 `text-transform: uppercase` + 字距~~ ✅ P4 已清除（Arco 全库只有 `text-transform: none`） | ~~**22 处**~~ 0 处 |
| 12 | 圆角 4 套并存：8px / 6px / 12px / 999px | `src/styles/themes.css:42`、`src/styles/buttons.css:16`、`src/styles/tables.css:124` |
| 13 | 浅色主题漏掉 `--token-input` / `--token-output` | 图表系列色不随主题切换 |
| 14 | ~~字体栈缺 CJK，且内置 Inter 只有 latin 子集~~ ✅ P4 已修：`--font-sans` 带完整 CJK 字族，`body` 与 `ChatMessage` 走 token | `src/styles/themes.css`（`--font-sans` / `--font-mono`） |
| 15 | 输入框无焦点环，`outline: none` 后仅改边框色 | `src/styles/tables.css:30-34` |

### 3.3 失效的 token 与颜色通道

| # | 问题 | 证据 | 后果 | P0 处置 |
|---|---|---|---|---|
| 16 | **`--chart-day-marker` 定义为 `var(--text)`**，而 `chartTheme.js` 的 `cssVar()` 返回的是**原始字符串** | `src/styles/themes.css:48` → `src/lib/utils/chartTheme.js:15` | Chart.js 收到字面量 `"var(--text)"`；Canvas 2D 对非法颜色值**静默忽略** —— "日边界标记线"从未画成注释承诺的高对比色。**现存静默 bug** | 改为字面量 `#E8E8E8` / `#1D2129`，并由守卫测试锁死 |
| 17 | **`--accent-strong` 被引用 8 次，却从未定义** | `SummaryCards.svelte`、`tables.css`、`SessionIDChip.svelte`、`page-globals.css`、`RequestEntrySummary.svelte`、`RequestEntryMetadata.svelte` | 8 处全部回落到 `--accent`；"比 accent 更强、用于浅底上的文字"这个意图从未生效 | 定义：浅色 `--primary-7`（7.70:1）、深色 `--primary-8`（8.26:1） |
| 18 | `--accent-hover` 定义 3 处，**零消费者** | `src/styles/themes.css` | 死 token | 删除 |
| 19 | `--info` 承载 **5 类互不相干的语义** | 见 §3.5 | 是"分类编码"与"主题色"混用的根源 | 拆分后删除（D24） |
| 20 | `budgets.css` **重复了 themes.css 的双写浅色模式** | `src/styles/budgets.css` | D4 消除重复时要一并处理，否则只改了一半 | 删除重复块 |

### 3.4 `--accent` 语义分布

按消费点归类（约 197 处消费侧）：

| 类别 | 占比 | 代表位置 | P0 处置 |
|---|---|---|---|
| 品牌色 / 主操作 | ~48% | `buttons.css:29`（主按钮底）、`tables.css:33`（焦点环）、`Sidebar.svelte:391`（导航激活）、`SegmentedControl.svelte:67`（选中段）、`DatePickerCalendar.svelte:205-223`（日期选中） | 保留；**填充位改指 `--primary`**，文字位走 `--accent` → `--primary-text` |
| 语义状态色 | ~28% | `forms.css:120-127`（**模型受限**）、`WorkflowChart.svelte` 21 处（连接线 / feature 节点 / 护栏流）、`SessionIDChip.svelte:47-88`（会话芯片）、`RateLimitsPage.svelte:208/:297`（scope 色相） | 保留 primary，但**受限态改中性灰**（D5）；其余逐类改判留到 P1/P2 |
| 装饰 / 区分性 | ~24% | `base.css:33`（通用徽标）、`forms.css:19`（别名类型徽标）、`NoDataIllustration.svelte:20`（插画描边）、`HighlightText.svelte:32`（搜索命中） | 随 primary 改色，无需单独处理 |

**结论：语义状态类 + 装饰类合计超过一半**，是重构时最大的语义混淆面。

### 3.5 `--info` 的 5 类语义

`--info` 原有 53 处引用（35 处是 themes.css 内的派生），消费侧 18 处，**分属 5 个不相干的用途**：

| 用途 | 位置 | 类别编码角色 | P0 处置 |
|---|---|---|---|
| 工作流"当前执行"节点 | `WorkflowChart.svelte`（10 处） | 与 feature 节点（用 accent）并列，**必须异色** | → `--category-1` |
| provider scope 色相 | `RateLimitsPage.svelte:231/:315`（`--scope-hue: var(--info)`） | 与 global scope（accent）、model scope（`#68765c`）并列，**必须异色** | → `--category-1` |
| playground assistant 角色 | `PlaygroundMessage.svelte`、`PlaygroundToolbar.svelte` | 可指向 primary | → `--primary-text` |
| 请求日志条目底色 / 计数 | `RequestEntrySummary.svelte`、`RequestLogPaneTabs.svelte` | 可指向 primary | → `--primary` / `--primary-text` |
| 本地缓存图表系列 | `overviewChartLogic.js`（`fade("var(--info)", 35)`） | 属数据色，见 §4.5 | → `--primary` |

> **因此 D10（`--info` 合并为 `--primary`）不成立** —— 合并后工作流的"当前执行"节点会与 feature 节点同色、provider scope 会与 global scope 同色，两个分类编码同时失效。改为拆出独立的 `--category-1`。

### 3.6 硬编码颜色盘点

`src/` 下（排除 themes.css）实际硬编码颜色 **71 处**（已剔除 3 处误报：issue 号 `#900`、HTML 实体 `&#9888;`）。

| 类别 | 处数 | 处置 |
|---|---|---|
| 与主题色重复 | 7 处 | P0 收敛为 token：`alerts.css`（rgba 形式的 warning）、`FlashMessages.svelte`（success / warning 的 rgba）、`AuthKeyList.svelte` 与 `UserList.svelte`（`var(--danger, #c0392b)` 的 fallback） |
| 跨语义硬编码 | 1 处 | `buttons.css` —— 棕色主按钮上的蓝色辉光（缺陷 #9），P0 直接删除 |
| **`#fff` 作前景色** | **14 处** | **P0 改为 `--on-fill`**（浅 `#FFFFFF` / 深 `#17171A`）：`base.css`、`buttons.css` ×2、`SegmentedControl.svelte`、`DatePicker.svelte`、`DatePickerCalendar.svelte` ×2、`Sidebar.svelte` ×2、`ThemeToggle.svelte`、`ModelsPage.svelte`、`CacheMeter.svelte`（→ `--meter-label`）、`UsageBreakdownChart.svelte`、`BudgetList.svelte` |
| `#fff` 作**底色**（开关旋钮） | 3 处 | P0 改为 `--switch-thumb`（`forms.css`、`ProviderStatusSection.svelte`、`ConversationDrawer.svelte`） |
| 分类色板（图表 / 周期） | 41 处 | **保留**：预算周期色板（`budgets.css` 30 处）、图表分类色板（`chartTheme.js` 11 色）、scope 色（`RateLimitsPage.svelte` 的 `#68765c`）。周期色板与 scope 色是两套不同语义碰巧同色，不是重复定义 |
| 纯黑阴影 / 中性灰 fallback | 约 29 处 | 与主题无关，保持 |

---

## 4. 色彩体系

### 4.1 两层结构

```
第 1 层  PALETTE   Arco 官方色阶，每族 10 级，每主题一套
第 2 层  SEMANTIC  扁平语义名（--bg / --text / --primary / --success …），指向第 1 层
```

**深色模式的色阶方向是反的**：level 1 最暗、level 10 最亮。这是 Arco 深色调色板的固有设计，**不能把浅色值直接搬到深色**。

| 族 | 浅色 level-6 | 深色 level-6 | 浅色 level-1 → 10 方向 |
|---|---|---|---|
| primary (arcoblue) | `#165DFF` | `#3C7EFF` | 浅→深 |
| success (green) | `#00B42A` | `#27C346` | 浅→深 |
| warning (orange) | `#FF7D00` | `#FF9626` | 浅→深 |
| danger (red) | `#F53F3F` | `#F76965` | 浅→深 |
| neutral | gray-1 `#F7F8FA` → gray-10 `#1D2129` | dark-gray-1 `#17171A` → dark-gray-10 `#F6F6F6` | level 1 最贴近背景（浅色最浅、深色最暗），level 10 对比最高 |
| cyan (category) | `#14C9C9` | `#3FD4CF` | 浅→深 |

### 4.2 浅色模式语义 token

| Token | 值 | 来源 / Arco 对应 | 对比度 |
|---|---|---|---|
| `--bg` | `#F7F8FA` | gray-1（页面底） | — |
| `--bg-surface` | `#FFFFFF` | bg-1（卡片 / 输入框 / 弹层） | — |
| `--bg-surface-hover` | `#F2F3F5` | gray-2（行悬停 / 悬浮底） | — |
| `--border` | `#E5E6EB` | gray-3 / border-2（分隔线、卡片描边） | — |
| `--border-strong` | `#C9CDD4` | gray-4 / border-3（输入框描边） | 1.59:1（见 §7） |
| `--text` | `#1D2129` | gray-10 / text-1 | 16.13:1 on surface |
| `--text-muted` | `#646E7D` | 偏离 gray-7，见 §7 | 4.65:1 on 最差底 |
| `--text-disabled` | `#A9AEB8` | gray-5 / text-4 | 豁免 |
| `--primary` | `#165DFF` | primary-6（填充 / 描边） | 5.19:1 on surface |
| `--primary-hover` | `#4080FF` | primary-5 | — |
| `--primary-text` | `#165DFF` | primary-6（主色作为文字） | 4.89:1 on 页面底 |
| `--accent` | → `--primary-text` | 兼容别名 | 5.19:1 on surface |
| `--accent-strong` | `#0E42D2` | primary-7 | 7.70:1 on surface |
| `--on-fill` | `#FFFFFF` | 饱和底上的文字 | 5.19:1 on primary |
| `--on-dark-fill` | `#FFFFFF` | 深色实底上的文字（预算周期色板） | — |
| `--success` | `#008026` | success-8（文字安全级，见 D22） | 4.84:1 on success-1 |
| `--success-text` | `#008026` | success-8 | 4.84:1 on success-1 |
| `--success-bg` | `#E8FFEA` | success-1 | — |
| `--warning` | `#A64500` | warning-8 | 5.68:1 on warning-1 |
| `--warning-text` | `#A64500` | warning-8 | 5.68:1 on warning-1 |
| `--warning-bg` | `#FFF7E8` | warning-1 | — |
| `--danger` | `#CB272D` | danger-7 | 4.77:1 on danger-1 |
| `--danger-text` | `#CB272D` | danger-7 | 4.77:1 on danger-1 |
| `--danger-bg` | `#FFECEC` | danger-1 | — |
| `--link` | `#165DFF` | link-6 = primary-6 | 5.19:1 |
| `--category-1` | `#03616C` | cyan-9（分类编码，见 §7） | 6.74:1 on 页面底 |
| `--meter-label` | `#17171A` | 仪表条切片上的文字 | 5.57:1 on 未命中切片 |
| `--switch-thumb` | `#FFFFFF` | 开关旋钮 | — |

### 4.3 深色模式语义 token

| Token | 值 | 对比度 |
|---|---|---|
| `--bg` | `#17171A` | — |
| `--bg-surface` | `#232324` | — |
| `--bg-surface-hover` | `#2A2A2B` | — |
| `--border` | `#333335` | — |
| `--border-strong` | `#484849` | — |
| `--text` | `rgba(255,255,255,.9)` | 12.93:1 on surface |
| `--text-muted` | `rgba(255,255,255,.7)` | 8.36:1 on surface |
| `--text-disabled` | `rgba(255,255,255,.3)` | 豁免 |
| `--primary` | `#3C7EFF` | primary-6（填充 / 描边） |
| `--primary-hover` | `#306FFF` | — |
| `--primary-text` | `#689FFF` | primary-7（文字） | 5.98:1 on surface |
| `--accent` | → `--primary-text` | 兼容别名 | 5.98:1 on surface |
| `--accent-strong` | `#93BEFF` | primary-8 | 8.26:1 on surface |
| `--on-fill` | `#17171A` | 饱和底上的文字 | 4.79:1 on primary |
| `--on-dark-fill` | `#FFFFFF` | 深色实底上的文字 | — |
| `--success` | `#27C346` | success-6（深底上 level-6 即安全） | 6.71:1 on surface |
| `--success-text` | `#50D266` | success-7 | 5.85:1 on success-bg |
| `--success-bg` | `rgba(39,195,70,.18)` | — |
| `--warning` | `#FF9626` | warning-6 | 7.21:1 on surface |
| `--warning-text` | `#FFB357` | warning-7 | 6.29:1 on warning-bg |
| `--warning-bg` | `rgba(255,150,38,.18)` | — |
| `--danger` | `#F76965` | danger-6 | 5.36:1 on surface |
| `--danger-text` | `#F98D86` | danger-7 | 5.27:1 on danger-bg |
| `--danger-bg` | `rgba(247,105,101,.18)` | — |
| `--link` | `#689FFF` | primary-7 | 5.98:1 |
| `--category-1` | `#3FD4CF` | dark cyan-6 | 9.82:1 on 页面底 |
| `--meter-label` | `#FFFFFF` | 仪表条切片上的文字 | 4.95:1 on 未命中切片 |
| `--switch-thumb` | `#FFFFFF` | 开关旋钮 | — |

### 4.4 状态色规则

Arco 的 tag / alert 规则是 **level-1 做底 + 高等级做字**，而不是拿状态色本身当文字色。

| 状态 | 浅色文字 / 底 | 深色文字 / 底 |
|---|---|---|
| 成功 | `#008026` / `#E8FFEA` | `#50D266` / `rgba(39,195,70,.18)` |
| 警告 | `#A64500` / `#FFF7E8` | `#FFB357` / `rgba(255,150,38,.18)` |
| 错误 | `#CB272D` / `#FFECEC` | `#F98D86` / `rgba(247,105,101,.18)` |

**为什么浅色取 level-8 而不是 level-6**：success-6 `#00B42A` 在白底上仅 2.78:1，warning-6 `#FF7D00` 仅 2.57:1，danger-6 `#F53F3F` 仅 3.71:1；取 level-8 / level-7 后分别为 4.84 / 5.68 / 4.77:1。深色主题反过来，level-6 在深底上本身就够（6.71 / 7.21 / 5.36:1），状态底色才升到 level-7。

**`--success` / `--warning` / `--danger` 三个 token 在浅色主题下直接取文字安全级**（D22）。全站有 60+ 处把它们当文字色用，如果 token 停留在 level-6，那 60 多处都得改消费者；让 token 本身安全，改动面小一个数量级。

### 4.5 图表数据色（与主题色解耦）

图表用色**不是**主题色。现有 token 吞吐系列以"冷暖 + 深浅"编码成本高低：

| Token | 浅色 | 深色 | 语义 |
|---|---|---|---|
| `--token-input` | `#C0824A` | `#D18F52` | 输入 token（付费，深暖） |
| `--token-output` | `#DDB27A` | `#E8BD8B` | 输出 token（付费，浅暖） |
| `--chart-prompt` | `#4080FF` | `#689FFF` | prompt 缓存命中（独立数据蓝） |
| `--chart-local` | primary-5 的 20% | primary-7 的 20% | 本地缓存命中（无 provider 成本） |
| `--token-prompt` / `--token-local` | 由 `--chart-prompt` 派生 | 同左 | 与上表对应 |

**缓存仪表条的例外**（D25）：仪表条的百分比标签要压在切片上，切片明暗随主题翻转，因此

| Token | 浅色 | 深色 | 说明 |
|---|---|---|---|
| `--meter-label` | `#17171A` | `#FFFFFF` | 切片上的百分比文字 |
| `--cache-meter-uncached` | `#C0824A` | `#96662F` | 深色取更深一档，才能压住白字（4.95:1） |

图表线需要 ≥3:1 于深色画布（要亮），仪表条要能压住白字（要暗），两者无法同值，所以仪表条的"未命中"色与 `--token-input` 解耦。

---

## 5. 组件规格

全部取自 Arco 编译产物实测值，非记忆值。

| 组件 | 现状 | 目标 | 阶段 |
|---|---|---|---|
| 按钮 | ✅ `padding 6px 16px` / 13px / 圆角 6px / 带辉光阴影 → 高 **32px** / `padding 0 15px` / **14px** / 圆角 **2px** / 无阴影 / `transition: all .1s cubic-bezier(0,0,1,1)` | — | P1 已完成 |
| 输入框 | ✅ 尺寸 / 圆角 / 14px 已达标（`forms.css`、`tables.css`、选择器、日期与弹窗输入）；焦点环已覆盖输入类 | — | P1 / P2 已完成 |
| 表格单元格 | ✅ `padding: 9px var(--space-cell)`（水平 16px） | — | P1 已完成 |
| 表头 | ✅ 14px / weight 500 / 不 uppercase / `text-muted`（`.data-table th`） | — | P1 已完成 |
| 状态标签 | ✅ 高 24px / `padding 0 8px` / 圆角 2px / 12px / weight 500（请求日志与 API 密钥两处状态徽章） | — | P1 已完成 |
| 卡片 | ✅ 圆角 **4px** / padding **16px**（`.card` 走 `--space-card`；供应商状态卡同步 16px） | — | P3 已完成 |
| 弹窗 | ✅ 圆角 **4px**；宽 440px（登录）/ 760px（编辑器），Arco 默认 520px | — | P3 已完成（宽度保留应用自身取舍） |
| 圆角阶 | ✅ 8 / 6 / 12 / 999 四套 → **控件 2px · 面板 4px · 胶囊 999px · 正圆 50%**（四个 token，见 §5.2） | — | P3 已完成 |
| 间距 | ✅ 语义变量含 18px / 14px 破栅格 → 统一 **4px 基准**：4 / 8 / 12 / 16 / 20 / 24 / 32（Arco 自身度量的两处例外见 §7 #12） | — | P3 已完成 |
| 字体栈 | ✅ `Inter + 系统西文`（缺 CJK，中文回退到随机系统字体） | `--font-sans`：Arco 正文字栈原样（含 PingFang SC / Hiragino Sans GB / Noto Sans / Microsoft YaHei）；`--font-mono`：`"SF Mono", Menlo, Consolas, monospace` | P4 已完成 |
| 正文字号 | 12 / 13 / 14 混用 | 正文 **14** / 辅助 **12** / 标题 **16 · 20 · 24** | P4 |

### 5.1 交互状态

- **焦点**：所有可聚焦控件统一 `--primary` 边框 + 2px 外环。输入类用 `:focus`（点进去就该看得见），按钮与其余控件用 `:focus-visible`（鼠标点击不画环）。环只有一个 token：`--primary-ring`（primary-8 实色）。外环由零偏移 box-shadow 画出，落在边框盒**外侧**的周围表面上（页面 / 卡片 / 浮层），深色主题对所有这些底 ≥6.84:1、浅色 ≥10.29:1。曾被分出的 `--ring-on-fill` 把填充按钮的环画成页面底色——环在页面底上完全不可见（CodeRabbit 在 #1618 复核时发现），已废除。
- **悬停**：行悬停用 `--bg-surface-hover`；链接 / 文字按钮用 `--primary-text`；填充按钮用 `--primary-hover`。
- **按下**：填充按钮用 `--primary-active`；按压瞬间关掉过渡（Arco `.arco-btn:active { transition: none }`），状态变化不等动画。
- **禁用**：`--disabled-opacity`（0.45）+ `--text-disabled` 文字 + `not-allowed` 光标，全应用一致；不参与对比度判定（WCAG 豁免）。
- **动效**：仅在"操作引发状态变化"时使用（展开、确认、提交中）；页面加载不编排入场动画。全局 `prefers-reduced-motion` 兜底在 `base.css`。

### 5.2 两条度量尺度

P3 把散落的数值收敛成两条规则，规则由 `tests/designGrid.test.js` 守卫。

**圆角**（`themes.css` 的四个 token，全应用只用这四个值）

| token | 值 | 用在哪 | Arco 对应 |
|---|---|---|---|
| `--radius-control` | 2px | 按钮、输入、标签、图标按钮、菜单项、表格操作按钮 | `--border-radius-small` |
| `--radius-panel` | 4px | 卡片、弹窗、浮层、下拉菜单、表格容器、区块 | `--border-radius-medium` |
| `--radius-pill` | 999px | 状态徽章、计数胶囊 | ——（Arco 用 `calc(h/2)`，等效） |
| `--radius-circle` | 50% | 状态圆点、加载圈、开关旋钮、正方形图标底座 | `--border-radius-circle` |

Arco 还定义了 `--border-radius-large: 8px`，但编译产物里**没有任何组件用它**（唯一字面 `8px` 是 `.arco-switch-type-line`），所以本项目不设这一级：方角只有 2 / 4 两档，其余交给胶囊与正圆。

**间距**（4px 基准）

所有 `padding` / `margin` / `gap` 取 4 / 8 / 12 / 16 / 20 / 24 / 32。两类值不在栅格上，且是**有意**的：

- **1px / 2px** —— 光学微调：图标在更高的盒子里居中、描边与相邻元素重叠。
- **Arco 组件自身的度量**（§7 #12）：按钮横向内边距 15px、表格单元格纵向内边距 9px。

---

## 6. 对比度实测

由 §4 token 表直接解析计算（`tests/themeTokens.test.js` 的同一套算法：先剥注释 → 递归解析 `var()` → 半透明底先与 surface 合成 → 半透明字再与合成后的底合成）。

### 浅色

| 组合 | 对比度 | 判定 |
|---|---|---|
| 正文 `#1D2129` / surface `#FFFFFF` | 16.13 | 通过 AA |
| 正文 / 页面底 `#F7F8FA` | 15.18 | 通过 AA |
| muted `#646E7D` / surface | 5.16 | 通过 AA |
| muted / 页面底 | 4.86 | 通过 AA |
| muted / 行悬停底 `#F2F3F5` | 4.65 | 通过 AA |
| 主色文字 `#165DFF` / surface | 5.19 | 通过 AA |
| 主色文字 / 页面底 | 4.89 | 通过 AA |
| accent `#165DFF` / surface | 5.19 | 通过 AA |
| accent-strong `#0E42D2` / surface | 7.70 | 通过 AA |
| success 文字 `#008026` / success 底 | 4.84 | 通过 AA |
| warning 文字 `#A64500` / warning 底 | 5.68 | 通过 AA |
| danger 文字 `#CB272D` / danger 底 | 4.77 | 通过 AA |
| 主按钮 白字 / `#165DFF` | 5.19 | 通过 AA |
| danger 底 白字 / `#CB272D` | 5.43 | 通过 AA |
| 仪表条深字 / `#C0824A` | 5.57 | 通过 AA |
| 分类色 `#03616C` / 页面底 | 6.74 | 通过 AA |
| 主按钮悬停 白字 / `#0E42D2` | 7.70 | 通过 AA |
| 主按钮按下 白字 / `#072CA6` | 10.94 | 通过 AA |
| 焦点环 `#072CA6` / 页面底 | 10.29 | 通过 3:1 非文本门槛 |
| 焦点环 / surface | 10.94 | 通过 3:1 非文本门槛 |
| 焦点环 / 行悬停底 | 9.85 | 通过 3:1 非文本门槛 |
| 填色按钮环 页面底白 / `#165DFF` | 4.89 | 通过 3:1 非文本门槛 |
| 填色按钮环 / 悬停填充 `#0E42D2` | 7.24 | 通过 3:1 非文本门槛 |
| 填色按钮环 / 按下填充 `#072CA6` | 10.29 | 通过 3:1 非文本门槛 |

### 深色

| 组合 | 对比度 | 判定 |
|---|---|---|
| 正文 / surface `#232324` | 12.93 | 通过 AA |
| 正文 / 页面底 `#17171A` | 14.58 | 通过 AA |
| muted / surface | 8.36 | 通过 AA |
| muted / 行悬停底 | 7.81 | 通过 AA |
| 主色文字 `#689FFF` / surface | 5.98 | 通过 AA |
| 主色文字 / 页面底 | 6.81 | 通过 AA |
| accent `#689FFF` / surface | 5.98 | 通过 AA |
| accent-strong `#93BEFF` / surface | 8.26 | 通过 AA |
| success 文字 `#50D266` / success 底 | 5.85 | 通过 AA |
| warning 文字 `#FFB357` / warning 底 | 6.29 | 通过 AA |
| danger 文字 `#F98D86` / danger 底 | 5.27 | 通过 AA |
| 主按钮 `#17171A` / `#3C7EFF` | 4.79 | 通过 AA |
| danger 底 `#17171A` / `#F76965` | 6.10 | 通过 AA |
| 仪表条白字 / `#96662F` | 4.95 | 通过 AA |
| 分类色 `#3FD4CF` / 页面底 | 9.82 | 通过 AA |
| 主按钮悬停 `#17171A` / `#689FFF` | 6.81 | 通过 AA |
| 主按钮按下 `#17171A` / `#93BEFF` | 9.41 | 通过 AA |
| 焦点环 `#93BEFF` / 页面底 | 9.41 | 通过 3:1 非文本门槛 |
| 焦点环 / surface | 8.26 | 通过 3:1 非文本门槛 |
| 焦点环 / 行悬停底 | 7.55 | 通过 3:1 非文本门槛 |
| 填色按钮环 `#17171A` / `#3C7EFF` | 4.79 | 通过 3:1 非文本门槛 |
| 填色按钮环 / 悬停填充 `#689FFF` | 6.81 | 通过 3:1 非文本门槛 |
| 填色按钮环 / 按下填充 `#93BEFF` | 9.41 | 通过 3:1 非文本门槛 |

> 焦点环是非文本 UI 元素，门槛是 WCAG 1.4.11 的 3:1，不是文字的 4.5:1。
> 守护测试解析 CSS 时必须**先剥离注释**再切分声明，否则注释后的第一条声明会被吞掉；带 alpha 的背景与文字必须**先与已知底色合成**再计算，否则会得到错误结论。

---

## 7. 刻意偏离 Arco 的地方

偏离项一律记录在案，并附实测理由。

| # | 偏离 | 理由 |
|---|---|---|
| 1 | 浅色 muted 用 `#646E7D` 而非 Arco `text-3` `#86909C` | Arco 值在白底仅 3.24:1；本值在全部浅色底上 ≥4.65:1 |
| 2 | 浅色状态文字取 level-8 / level-7 而非 Arco 的 level-6 | Arco 的 green-6 `#00B42A` 在白底仅 2.78:1 |
| 3 | 深色主按钮用深色字 `#17171A` | 白字在 `#3C7EFF` 上仅 3.74:1 |
| 4 | 卡片圆角 4px（Arco Card 基础为 0） | 延续现有卡片形态，避免与 P1–P4 的控件改造混在一起 |
| 5 | 图表保留暖棕数据色，不引入 `data-1..20` | 成本编码依赖"冷暖 + 深浅"，非色相体系 |
| 6 | **输入框边框 1.59:1，未达 WCAG 1.4.11 的 3:1** | 与 Arco 官方 Input 一致；另有 label、聚焦环、填充差三重线索。**此偏差在守卫测试中显式豁免并记录**，若未来要消除，须改用 `#86909C`（白底 3.24:1）并接受边框变重 |
| 7 | **分类色取 cyan-9（浅）/ dark cyan-6（深），而非统一的 cyan-6** | cyan-6 `#14C9C9` 在白底仅 2.05:1，而 `--category-1` 要同时当工作流节点文字与 scope 色相用；深色主题的 cyan-6 是 `#3FD4CF`（9.82:1），两主题取各自的安全级 |
| 8 | **深色主题的状态色停在 level-6，浅色主题升到 level-8** | level-6 在深底上够用（6.71 / 7.21 / 5.36:1），在浅底上不够（2.78 / 2.57 / 3.71:1）。与其按主题挑不同的"正确色阶"，不如让 token 本身在两个主题下都安全 |
| 9 | **深色主题的 `--accent-strong` 取 primary-8 而非 primary-7** | `--accent` 已指向 primary-7；强调色必须真的"更强"，深色下"更强"= 更亮（8.26:1） |
| 10 | **填充按钮的 hover/active 取 primary-7 / primary-8，而非 Arco 的 5 / 7** | Arco 用白字，其深色 hover 填充 `#306FFF` 上白字仅 4.36:1、浅色 `#4080FF` 上白字仅 3.71:1，都低于 AA。本项目深色主题用深色字（`--on-fill`），两主题都取"文字安全侧"的 7/8 级——色阶方向相反，7 级在深色是更亮一档、在浅色是更暗一档，两边都 ≥6.8:1 |
| 11 | **焦点环统一用 `--primary-ring`（primary-8 实色）** | Arco 的环（深色 `rgba(primary-6,.35)` / 浅色实心 primary-3）实测只有 ~1.6:1，达不到 WCAG 1.4.11 焦点指示器的 3:1。环像素画在边框盒外侧的周围表面上，primary-8 对页面 / 卡片 / 浮层全部 ≥6.84:1（深）、≥10.29:1（浅）。曾按「环落在填充上」分出过 `--ring-on-fill`（页面底色），导致填充按钮在页面底上环不可见，已统一并废除 |
| 12 | **Arco 自身的两个组件度量不落在 4px 栅格上：按钮横向内边距 15px、表格单元格纵向内边距 9px** | 4px 栅格管的是**应用自己的布局间距**（间隙、内边距、外边距），不是从 Arco 实测来的组件度量——整个设计体系的对齐基准就是这些实测值（§5 开头："全部取自 Arco 编译产物实测值"）。改成 16px / 8px 会让按钮不再是 Arco 的按钮、表格不再是 Arco 的表格，1px 的收益抵不上这个代价。守卫测试 `tests/designGrid.test.js` 对这两处**逐条列出豁免**（文件 + 选择器 + 完整声明），其余任何越界值都会报错 |

---

## 8. 落地计划

| 阶段 | 内容 | 主要文件 | 风险 |
|---|---|---|---|
| **P0** ✅ | Token 层重建：两层结构、新增语义 token、修正主题切换机制、补齐深浅两套图表色、修正对比度不达标的状态样式、受限态改中性灰 | `src/styles/themes.css`、`src/lib/stores/ui.svelte.js`、`index.html`、`src/styles/forms.css`、`src/styles/page-globals.css` 等 | 中 —— 视觉变化最大，但不改组件结构 |
| **P1** ✅ | 控件规格统一：按钮 / 输入 / 表格 / 标签尺寸、圆角、字阶；去掉按钮辉光阴影 | `buttons.css`、`tables.css`、`forms.css`、`usage-requests.css`、`page-globals.css`、`SegmentedControl` / `SearchPopover` / `DatePicker` / `AuthDialog` | 低 |
| **P2** ✅ | 焦点环与三态补齐：`focus-visible` 兜底、hover / active / disabled | `base.css`、`buttons.css`、`tables.css`、`forms.css`、`themes.css` | 低 |
| **P3** ✅ | 间距改 4px 栅格，圆角收敛为 2 / 4 / 胶囊 / 正圆四档（§5.2） | `base.css`、`themes.css`、85 个模块与组件文件 | 低 |
| **P4** ✅ | 去 uppercase（32 处）、字体栈补 CJK | `themes.css`、`base.css`、`forms.css`、`WorkflowChart` 等 30 个文件 | 低 |

### 8.1 P0 落地清单（已实施）

1. **重建 `src/styles/themes.css`**
   - 第 1 层：primary / success / warning / danger / neutral / cyan 六族 10 级色阶，深浅各一套（深色色阶方向反转）。
   - 第 2 层：§4.2 / §4.3 的语义 token，全部以 `var()` 别名指向第 1 层。
   - `--accent` 指向 `--primary-text`（201 处引用零变更）；新增 `--accent-strong`。
   - 补齐 `--border-strong`、`--text-disabled`、`--primary`、`--primary-hover`、`--primary-text`、`--on-fill`、`--on-dark-fill`、`--success-bg/-text`、`--warning-bg/-text`、`--danger-bg/-text`、`--link`、`--category-1`、`--meter-label`、`--switch-thumb`、`--chart-prompt`、`--chart-local`。
   - **补齐浅色主题的 `--token-input` / `--token-output`**（缺陷 #13）。
   - **删除 `@media (prefers-color-scheme: light)` 整块**（与 `[data-theme="light"]` 完全重复）；同理删除 `src/styles/budgets.css` 的重复浅色块（缺陷 #20）。
   - **6 个 `--chart-*` token 保持字面量**（D18）。
   - **删除 `--accent-hover`（死 token）与 `--info`（已拆分）**。
2. **改 `src/lib/stores/ui.svelte.js` 的 `themeStore`**
   - `apply()` 在 `system` 模式下也写入 `data-theme`（解析后的实际模式），不再移除属性。
   - 监听 `matchMedia("(prefers-color-scheme: dark)")` 的 `change`，系统切换时重写 `data-theme` 并 bump `tick`（图表随之重建）—— 保持 D12 的实时跟随能力。
3. **`public/theme-init.js` 首屏主题脚本**
   - 在样式表之前同步解析 `localStorage.gomodel_theme` 与 `prefers-color-scheme`，写入 `data-theme`，避免首屏闪一下错误配色。
   - 必须是外部脚本（index.html 以 `%BASE_URL%theme-init.js` 同步引入）：网关 CSP 的 `script-src` 只 hash-放行 Go 注入的 runtime globals，内联版会在真实实例上被拦（§10 复核时由 console 证实）。
4. **修对比度不达标的状态样式**（D11）
   - `page-globals.css` `.auth-key-status-active` / `.auth-key-status-inactive`、`.copy-feedback-btn-copied` → `--success-bg` / `--success-text` / `--danger-bg` / `--danger-text`。
   - `forms.css` `.alias-kind-badge` → `--primary-text`。
   - `alerts.css`、`.flash-toast-success` / `.flash-toast-error` 的 rgba 硬编码 → `--warning-bg` / `--warning-text` 等。
   - `AuthKeyList.svelte`、`UserList.svelte` 的 `var(--danger, #c0392b)` fallback → `--danger-text`。
5. **14 处 `#fff` 前景色 → `--on-fill`**（深色主题下白字在 `#3C7EFF` 上仅 3.74:1）；3 处 `#fff` 底色（开关旋钮）→ `--switch-thumb`；缓存仪表条标签 → `--meter-label`。
6. **受限态改中性灰**（D5）：`forms.css` `.alias-toggle.restricted` → `--border-strong` / `--text-muted` / `--bg-surface-hover`。
7. **修现存失效 token**（§3.3）：定义 `--accent-strong`；`--chart-day-marker` 改字面量；删除 `--accent-hover`。
8. **`--info` 按 D17 拆分并删除**：`WorkflowChart.svelte`（10 处）与 `RateLimitsPage.svelte`（2 处）→ `--category-1`；其余 3 类 → `--primary` / `--primary-text`。
9. **新增守卫测试** `web/dashboard/tests/themeTokens.test.js`
   - 解析 `src/styles/themes.css`（先剥注释），解析 `var()` 别名，按 §6 的组合表断言 ≥4.5:1（32 组，含半透明合成）。
   - 额外断言：6 个 `--chart-*` 必须是**可解析的颜色字面量**；浅色调色板只定义一次；`budgets.css` 不再重复浅色块；`--accent-strong` / `--category-1` 等已定义、`--accent-hover` / `--info` 已删除。
10. **文档**：`CONVENTIONS.md` 增加"设计 token"一节。

### 8.2 P1 落地清单（已实施）

1. **按钮**（`buttons.css`）：`.btn` → 高 32px / `padding 0 15px` / 14px / 圆角 2px / `transition: all .1s cubic-bezier(0,0,1,1)`；删除 `.btn-danger` 的辉光阴影。
2. **输入**（`forms.css`、`tables.css`）：`textarea` / `.form-input` / 表格内文本输入 → 高 32px（textarea 除外）/ `padding 0 12px` / 14px / 圆角 2px；新增共享焦点环 `:is(input,textarea,select):focus`（主色边框 + `color-mix` 12% 外环）。
3. **选择器**（`usage-requests.css`）：`.form-select` / `.usage-log-select` → 高 32px / `padding 0 34px 0 12px` / 14px / 圆角 2px / `background-color: var(--bg-surface)`。
4. **表格**（`tables.css`）：`.data-table th` → 14px / weight 500 / 无 uppercase；`.data-table td` → `padding: 9px var(--space-cell)`；`.table-action-btn` / `.table-icon-btn` → 圆角 2px。
5. **状态标签**：`.request-log-status-badge`（`usage-requests.css`）与 `.auth-key-status-badge`（`page-globals.css`）→ 高 24px / `padding 0 8px` / 圆角 2px / 12px / weight 500。
6. **组件控件**：`SegmentedControl.svelte`（容器与按钮圆角 2px）、`SearchPopover.svelte` 触发器（圆角 2px / 14px）、`DatePicker.svelte` 触发器（高 32px / 圆角 2px / 14px）、`AuthDialog.svelte` 输入框（高 32px / 圆角 2px / 焦点环与全局一致）。

### 8.3 P2 落地清单（已实施）

1. **焦点环统一**（`base.css`）
   - 全局 `:focus-visible` 兜底：`outline: 2px solid var(--primary-ring)`，链接、菜单项、表格排序头与后续新组件都自动有环。
   - 输入类保留 `:focus` 的主色边框 + 环（`forms.css`），`--primary-ring` 取代此前的 12% alpha——12% 实测只有 ~1.1:1，等于没有环。
   - 按钮用 `box-shadow: 0 0 0 2px`（`buttons.css`），避免 outline 参与布局。
2. **散落的自定义环收敛**（11 个文件）：`Sidebar`、`ThemeToggle`、`DialogCloseButton`、`ProviderStatusCard`、`ProviderStatusSection`、`SummaryCards`、`ModelRow`、`ModelTable`、`ChatMessage`、`ConversationDrawer`、`DemoModeBanner`、`auth-dialog.css`、`settings.css`、`WorkflowChart`、`WorkflowIdBadge` 的 28–45% alpha 环统一改用 `--primary-ring`。保留三处有意的数据色环：prompt-cache 青、rate-limit scope 色、workflow 节点主色。
3. **三态补齐**
   - hover/active：填充按钮走 `--primary-hover` / `--primary-active`（新增 token）；`.btn-danger` 补 `:active`；`.table-action-btn` / `.alias-toggle` / `.segmented-btn` 补 `:active { transition: none }`（Arco 基类规则）。
   - disabled：新增 `--disabled-opacity`（0.45），8 处控件的 0.35 / 0.4 / 0.45 / 0.6 统一，并补 `--text-disabled` 文字与 `not-allowed` 光标。
4. **reduced-motion 全局兜底**（`base.css`）：动画时长、迭代次数、过渡时长、滚动行为全部归零；组件级"直接隐藏装饰"的规则保留。
5. **守卫测试扩容**（`tests/themeTokens.test.js`）
   - 新增 `--on-fill` 对 `--primary-hover` / `--primary-active` 两组断言（文字压在悬停/按下填充上）。
   - 新增焦点环断言：3:1 的非文本门槛（WCAG 1.4.11），`--primary-ring` 查 4 个底色（页面 / 卡片 / 悬停 / 浮层）。
   - 新增 token 存在性断言：`--primary-ring`、`--primary-hover`、`--primary-active`；并断言 `--ring-on-fill` 已删除。

### 8.4 P3 落地清单（已实施）

1. **圆角收敛为四个 token**（`themes.css`）
   - 新增 `--radius-control: 2px` / `--radius-panel: 4px` / `--radius-pill: 999px` / `--radius-circle: 50%`，删除 `--radius: 8px`。
   - 全应用 184 处 `border-radius` 全部改写成这四个 token 之一：60 处 `var(--radius)`（8px）按"控件 vs 面板"分流，36 处 `6px` 归控件，25 处 `999px` 归胶囊，8 处 `50%` 归正圆，其余按语义归位。
   - 分流的判据是 Arco 自己的用法：`.arco-modal` / `.arco-dropdown-menu` / `.arco-table-container` 用 medium（4px），`.arco-btn` / `.arco-tag` 用 small（2px），`.arco-avatar-circle` / spinner 用 circle（50%）。
   - 少数特例按形状而非尺寸定档：开关轨道（34×18）与计数胶囊是胶囊，旋钮与状态点是正圆，表格里的 provider 标签是控件（2px）。
2. **间距改 4px 栅格**（`base.css` + 85 个模块与组件文件）
   - `--space-*` 全部落到 4 的倍数：桌面 32 / 24 / 16 / 16 / 24 / 16；平板 20 / 16 / 20；手机 12 / 12 / 12 / 8 / 16 / 8。
   - 337 处越界的 `padding` / `margin` / `gap` 按"就近取整"改写：3→4、5→4、6→8、7→8、9→8、10→12、11→12、14→16、15→16、18→20、22→24……（`6` 与 `10` 恰好落在两级之间，一律向上取，让留白偏松不偏挤）。
   - 1px / 2px 保留：它们是光学微调（图标居中、描边重叠），不是间距。
   - 负内边距与成对的外边距一起改写，抵消关系不变（如 `.provider-group-toggle` 的 `padding: 8px 8px` / `margin: -8px -8px`）。
   - `.table-action-btn` 由 `padding: 6px 12px` 改成 `height: 32px` + `padding: 0 12px`：栅格化后 8px 的纵向内边距会把行内按钮撑到 36px，与 32px 的 `.table-icon-btn` 错位；改回固定高度后与 Arco 的 `.arco-btn-size-default` 一致。
3. **两处 Arco 度量保留在栅格外**（§7 #12）：按钮 `padding: 0 15px`、表格单元格 `padding: 9px var(--space-cell)`。
4. **新增守卫测试** `web/dashboard/tests/designGrid.test.js`
   - 扫 `src/` 下所有 `.css` / `.svelte` 的 `<style>`：每条 `border-radius` 只能是四个 token 之一（或 `0` / `inherit`）。
   - 每条 `padding` / `margin` / `gap` 的 px 值必须是 4 的倍数（1px / 2px 除外），两处 Arco 度量按"文件 + 选择器 + 完整声明"逐条豁免。
   - 断言 `--space-*` 全部在栅格上，四个圆角 token 保持 Arco 实测值。

### 8.6 P4 落地清单（已实施）

1. **去 uppercase**（32 处 `<style>` 块）
   - 依据：Arco 编译产物中 `text-transform` 只出现一次——`button, select` 上的 `none`——没有任何组件做 uppercase，标签层级靠字重与颜色表达。
   - 删除全部 `text-transform: uppercase` 及其配套的装饰性 `letter-spacing`（0.06em / 0.1em / 0.2px / 0.5px 等）；保留非装饰的字距（`.card-value` 等大数字的负字距、prompt-cache 徽章的 0.02em 微字距，均不与 uppercase 配对）。
   - 涉及 26 个 `.svelte` 组件与 4 个共享样式文件；`usage-requests.css` 唯一保留的 `text-transform: none` 是显式覆盖，非装饰。
2. **字体栈补 CJK**（`themes.css` + `base.css`）
   - 新增 `--font-sans`：Arco 正文字栈原样（`Inter, -apple-system, BlinkMacSystemFont, "PingFang SC", "Hiragino Sans GB", "Noto Sans", "Microsoft YaHei", "Helvetica Neue", Helvetica, Arial, sans-serif`）；`body` 改用 `var(--font-sans)`，`ChatMessage.svelte` 的手写副本同步收敛。
   - 新增 `--font-mono`：`"SF Mono", Menlo, Consolas, monospace`；15 处已有的 `var(--font-mono)` 引用从"未定义 token"变为真解析。
   - **表单控件改为继承字栈**（`base.css`）：UA 样式表给 `button` / `input` / `select` / `textarea` 各自的默认字体（Arial），不继承 —— 页面上 733 个元素里只有 10 个（等宽）走对字体，其余按钮全部落在 Arial。Arco 的 reset 正是 `button, input, optgroup, select, textarea { font-family: inherit }`（编译产物实测），这里只继承字体族，字号仍由各控件自己定，避免改动度量。
   - 两个 Canvas 字符串（`chartTheme.js` 的 tick 字体、`liveTokensLogic.js` 的日标记）不能读 `var()`，保持字面量并加注释指向 `--font-mono`。
3. **新增守卫测试** `web/dashboard/tests/fonts.test.js`
   - 断言 `--font-sans` / `--font-mono` 存在且保持 Arco 实测值（忽略换行格式）。
   - 扫全库 `<style>` 与 `.css`：任何 `text-transform` 只允许 `none`。
   - 每条 `font-family` 必须是 `var(--font-*)` 或 `inherit`，不允许新的硬编码字栈。
   - 断言表单控件继承字体栈（防止 Arial 悄悄回来）。
   - 断言两个 Canvas 字符串与 `--font-mono` 同栈——改 token 必须同步改 JS。

### 8.7 验证

```sh
cd web/dashboard
npm run check    # svelte-check：零错误
npm test         # 含新增的 token 守卫测试
npm run build    # dist 是 go:embed 进二进制的，必须重建才生效
```

P3 后复测：svelte-check 零错误，1039 项测试通过（含 token 守卫 6 项 + 栅格守卫 4 项），构建产物含四个 `--radius-*` token，页面实测无越界间距。

P4 后复测：svelte-check 零错误，1044 项测试通过（新增字体守卫 5 项），构建产物含 `--font-sans` / `--font-mono` 与 CJK 字族名、零个 `text-transform: uppercase`；浏览器实测页面 733 个元素只有两个字体族（应用 sans 栈 + mono 栈），无 Arial 残留。

> `internal/admin/dashboard/static/dist` 由 `make frontend` 生成并 embed 进 Go 二进制；改完前端必须 `npm run build` 再 `go build`，否则改动不生效。

---

## 9. 风险与回归点

| 风险 | 说明 | 应对 |
|---|---|---|
| `chartTheme.js` 反读 token | 通过 `getComputedStyle` 读取自定义属性，token 一改图表配色即变 | 守卫测试断言 6 个图表 token 必须是字面量；**不得**用 `light-dark()` 或 `var()` 嵌套 `color-mix()` |
| `--accent` 语义混用 | 201 处引用里混着品牌色与语义状态色 | P0 保持别名不改；P1/P2 按 §3.4 表格逐类改判 |
| Svelte 作用域 hash | 共享 CSS 的作用域规则变化会反压 scoped 基础样式 | 按 `CONVENTIONS.md` 第 3 条，迁移规则时同步迁移状态变体与媒体查询 |
| 深色色阶方向反转 | 浅色值误用会导致深色主题整片偏暗 / 偏亮 | 深浅两套色阶写在同一文件相邻位置对照修改，不分两次提交 |
| 守卫测试误报 | CSS 注释、alpha 背景、`var()` 别名都会让朴素解析出错 | 测试先剥注释；alpha 背景与文字先与底色合成；别名需递归解析 |
| 状态色的"浅深不对称" | 浅色主题的状态色比深色主题暗两级，肉眼看会像两套不同的状态色 | 这是对比度的必然结果（§7 第 8 条）；两者都是 Arco 官方色阶，深色主题靠 level-7 的状态底色拉开层次 |
| 仪表条与图表色解耦 | `--cache-meter-uncached` 与 `--token-input` 不同色，图例色块与曲线不完全同色 | 深色主题的差异肉眼不可辨（都是暖棕）；换来的是切片上的百分比文字可读 |

---

## 10. 验收清单

- [x] `npm run check` 零错误，`npm test` 通过（1044 项）
- [x] 守卫测试能复现历史缺陷：把 `--text-muted` 改回 `#6B7785`、`--success-text` 改回 success-6、把 `--chart-day-marker` 改回 `var(--text)`，三者各自报错
- [x] 深浅两主题的组件取色已实测（按钮 / 徽标 / 状态胶囊 / 告警 / 表格 / 仪表条 / 工作流节点 / 图表色板）
- [ ] 深浅两主题 × 6 个关键页（概览 / 模型 / 请求日志 / 用量 / 预算 / 设置）共 12 张截图目视 —— **待真实网关实例上复核**
- [ ] 浅色模式系统切换到深色，图表与日历色阶同步重建，无残留旧色 —— 待真实实例复核（`themeStore.tick` 链路已改，逻辑上成立）
- [x] 焦点环全应用统一：输入类 `:focus`、其余 `:focus-visible`，两环均 ≥3:1（守卫测试断言）；12% 的旧环（~1.1:1）已废除
- [x] 浏览器实测（Playwright，真实构建产物）：浅色主按钮环 `rgb(247,248,250) 0 0 0 2px`、默认按钮 `rgb(7,44,166)`、链接 `rgb(7,44,166) 2px solid`；深色主题 token 解析为 `#93BEFF` / `#17171A`，页面上的分段控件走全局兜底得到 `rgb(147,190,255) 2px solid`
- [x] "受限"模型标记为中性灰，与主操作蓝明确可区分
- [x] `npm run build` 通过，产物中含新样式
- [x] P2 后复测：svelte-check 零错误、1035 项测试通过（含 token 守卫 6 项）、构建产物含 `--primary-ring`
- [x] 圆角收敛为四个 token（184 处 `border-radius` 全部改写），间距全部落在 4px 栅格上（337 处越界改写，仅保留 1px / 2px 光学微调与 §7 #12 的两处 Arco 度量）
- [x] 栅格守卫测试（`tests/designGrid.test.js`）能复现回归：注入 `border-radius: 7px` / `gap: 6px` 报错；把按钮的 `0 15px` 改成 `0 17px`（豁免不再匹配）同样报错
- [x] P4：全应用零个 `text-transform: uppercase`（守卫测试断言）；`--font-sans` 含 CJK 字族、`--font-mono` 落地，15 处引用真正解析；表单控件改为继承字体栈，页面无 Arial 残留；字体守卫测试（`tests/fonts.test.js`）能复现回归：注入 `text-transform: uppercase` 或硬编码字栈报错
- [x] 浏览器实测（Playwright，真实构建产物）：概览页 104 个不同样式的元素中越界间距为 0；`.card` 圆角 4px / padding 16px，`.nav-item` 圆角 2px，`.live-dot` 50%，`.chart-container` 圆角 4px / padding 24px；深浅两主题均正常

---

## 11. 附录

### 11.1 Arco 官方色阶全表

**arcoblue（primary）**

| 级 | 浅色 | 深色 |
|---|---|---|
| 1 | `#E8F3FF` | `#000D4D` |
| 2 | `#BEDAFF` | `#041B79` |
| 3 | `#94BFFF` | `#0E32A6` |
| 4 | `#6AA1FF` | `#1D4DD2` |
| 5 | `#4080FF` | `#306FFF` |
| 6 | `#165DFF` | `#3C7EFF` |
| 7 | `#0E42D2` | `#689FFF` |
| 8 | `#072CA6` | `#93BEFF` |
| 9 | `#031A79` | `#BEDAFF` |
| 10 | `#000D4D` | `#EAF4FF` |

**green（success）/ orange（warning）/ red（danger）**

| 级 | green 浅 | green 深 | orange 浅 | orange 深 | red 浅 | red 深 |
|---|---|---|---|---|---|---|
| 1 | `#E8FFEA` | `#004D1C` | `#FFF7E8` | `#4D1B00` | `#FFECEC` | `#4D000A` |
| 2 | `#AFF0B5` | `#046625` | `#FFE4BA` | `#793004` | `#FDCDC5` | `#770611` |
| 3 | `#7BE188` | `#0A802D` | `#FFCF8B` | `#A64B0A` | `#FBACA3` | `#A1161F` |
| 4 | `#4CD263` | `#129A37` | `#FFB65D` | `#D26913` | `#F98981` | `#CB2E34` |
| 5 | `#23C343` | `#1DB440` | `#FF9A2E` | `#FF8D1F` | `#F76560` | `#F54E4E` |
| 6 | `#00B42A` | `#27C346` | `#FF7D00` | `#FF9626` | `#F53F3F` | `#F76965` |
| 7 | `#009A29` | `#50D266` | `#D25F00` | `#FFB357` | `#CB272D` | `#F98D86` |
| 8 | `#008026` | `#7EE18B` | `#A64500` | `#FFCD87` | `#A1151E` | `#FBB0A7` |
| 9 | `#006622` | `#B2F0B7` | `#792E00` | `#FFE3B8` | `#770813` | `#FDD1CA` |
| 10 | `#004D1C` | `#EBFFEC` | `#4D1B00` | `#FFF7E8` | `#4D000A` | `#FFF0EC` |

**neutral**

| 级 | 浅色（gray） | 深色（dark-gray） |
|---|---|---|
| 1 | `#F7F8FA` | `#17171A` |
| 2 | `#F2F3F5` | `#2E2E30` |
| 3 | `#E5E6EB` | `#484849` |
| 4 | `#C9CDD4` | `#5F5F60` |
| 5 | `#A9AEB8` | `#78787A` |
| 6 | `#86909C` | `#929293` |
| 7 | `#6B7785` | `#ABABAC` |
| 8 | `#4E5969` | `#C5C5C5` |
| 9 | `#272E3B` | `#DFDFDF` |
| 10 | `#1D2129` | `#F6F6F6` |

**cyan（分类色）**

| 级 | 浅色 | 深色 |
|---|---|---|
| 1 | `#E8FFFB` | `#00424D` |
| 2 | `#B7F4EC` | `#06616C` |
| 3 | `#89E9E0` | `#11838B` |
| 4 | `#5EDFD6` | `#1FA6AA` |
| 5 | `#37D4CF` | `#30C9C9` |
| 6 | `#14C9C9` | `#3FD4CF` |
| 7 | `#0DA5AA` | `#66DFD7` |
| 8 | `#07828B` | `#90E9E1` |
| 9 | `#03616C` | `#BEF4ED` |
| 10 | `#00424D` | `#F0FFFC` |

Arco 另有独立的背景阶（深色）：`bg-1 #17171A` / `bg-2 #232324` / `bg-3 #2A2A2B` / `bg-4 #313132` / `bg-5 #373739`，与 neutral 阶并行使用。

### 11.2 可视对比稿

现状与目标并排的四面板对比稿（浅色 / 深色 × 现状 / Arco 化），含官方色阶与实测对比度表：

```
/tmp/gomodel-arco-spec/specimen.html
/tmp/gomodel-arco-spec/specimen-full.png
```

本地查看：`cd /tmp/gomodel-arco-spec && python3 -m http.server 8731` 后访问 `http://127.0.0.1:8731/specimen.html`。

> 该文件是**一次性沟通材料**，不进入仓库：手工维护的 HTML 规范页会与 `themes.css` 漂移，形成第二个假的事实源。

### 11.3 参考

- Arco Design 官网 — https://arco.design/
- 样式指南 — https://www.arco.design/docs/en-US/spec/style-guideline
- 设计价值观（清晰 / 一致 / 韵律 / 开放）— https://arco.design/docs/en-US/spec/values-of-arcodesign
- 设计哲学（务实浪漫主义）— https://arco.design/docs/en-US/spec/philosophy
- 官方色板配置 — https://arco.design/themes/design/5130/setting/base/Color

色值取自 `@arco-design/web-react@2.66.0` 的编译产物（`dist/css/arco.css` 与 `es/style/theme/color/colors.less`），非目测。
