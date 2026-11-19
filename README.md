# Folio

一款基于 Go 与 [MyGo](https://github.com/egoist/mygo) 构建的本地离线优先（Local-first）**MarginNote 笔记阅读、PDF 分屏批注与思维导图工作台**，面向 **Android 平板**（深度适配触控与压感手写笔）及 **macOS** 桌面端。

Folio 支持直接导入 MarginNote 导出的 `.marginpkg` / `.marginnotes` 文件以及独立 `.pdf` 文档，在本地以分片结构离线保存，提供多种脑图与大纲视图形态、PDF 留白手写批注、双向摘录联动以及带图 Markdown 导出能力。

---

## 核心特性

### 1. MarginNote 笔记导入与本地库管理
- **多格式导入**：支持 `.marginpkg`（自动解包 `.marginnotes` 数据库、附件图片、PencilKit 手写及附带 PDF 与文档目录）、`.marginnotes`（纯 Go SQLite 只读不可变模式解析）及独立 `.pdf` 文件。
- **高保真数据解析**：
  - 还原 MarginNote 节点层级树与共享/循环引用、16 套卡片主题配色（`mbooks-annotation*`）及节点折叠/布局配置。
  - 解析 `HtmlNote` 富文本样式（文字颜色、背景高亮、粗体、斜体、下划线与字号），并针对浅色/深色模式自动校准对比度。
  - 提取原图摘录（`Clip`）、PDF 高亮与遮罩坐标（`HighlightRects` / `MaskRects`）、文本/链接/图片与手写内容块（`TextNote`、`HtmlNote`、`LinkNote`、`PaintNote`、`PicNote`、`ImageNote`、`MergedImageNote`、`SketchNote`）、`#标签`、笔记互链（`marginnote*app://note/...`）以及文档目录（TOC）与上次阅读页码。
  - 跨平台还原 Apple PencilKit（`PKDrawing`）手写批注：macOS 优先调用 PencilKit 渲染器，同时内置纯 Go 的 `PKDrawing` Protobuf 解析与光栅化引擎，确保 Android 等非苹果平台同样完整显示手写内容。
- **增量合并与本地编辑**：再次导入同一主题的新版导出包时，自动保留本地修改过的笔记标题/正文、用户新建的子笔记及已删除记录；支持新建子笔记、编辑笔记、删除笔记（下级节点自动保留并上移）、重命名/删除笔记本，以及按标题、正文、摘录与标签进行多关键词即时搜索。
- **多种导入入口**：支持文件选择对话框、桌面窗口拖拽、命令行参数传入、macOS 系统文件关联，以及 Android 系统「打开方式 / 分享」（`ACTION_VIEW` / `ACTION_SEND` / `ACTION_SEND_MULTIPLE`，支持基于文件头魔数自动识别缺失扩展名的 Content URI）。

### 2. 多形态脑图与大纲视图
在「设置 → 脑图视图形态」中可随时切换四种视图：
- **卡片树（`cards`）**：富内容横向树状画布，直接展示卡片配色、富文本块、内联图片与页码跳转徽标；当同一分支下存在 3 个及以上连续未展开叶子节点时，自动通过动态规划均衡分列为二维网格，减少连线杂乱。
- **专题框架（`board`）**：递归嵌套的二维专题框架视图，顶层多框架采用 Skyline 二维紧凑排布，同时兼容在框架内以横向迷你树渲染带有树形 `layoutMode` 的子分支。
- **大纲流（`outline`）**：纵向缩进阅读流，支持按分支展开/折叠、一键「全部展开 / 收起下级」、标签展示、附图放大预览以及点击页码一键跳回 PDF。
- **精简树（`compact`）**：沿当前激活路径逐级展开的轻量标题树，适合快速浏览深层层级。

无限画布同时支持拖拽平移、滚轮/双指捏合缩放（10%–300%，含双指同步平移）、可开关的点阵参考背景、右下角交互式导航缩略图（Minimap）、选中卡片悬浮快捷操作栏（详情 / 编辑 / 子卡片 / 原图 / 折叠展开 / 删除）及居中详情模态卡片（含面包屑路径、关联笔记跳转、下级节点列表与原图全屏预览）。

### 3. PDF 阅读、留白批注与双向联动
- **三种工作区模式**：支持 **PDF**、**分屏**（宽屏左右分屏、窄屏/竖屏自动切换上下分屏，可拖拽分隔条调整比例）与 **画布** 之间自由切换。
- **完整阅读辅助**：支持多本 PDF 切换、文档目录弹窗（TOC）、上下翻页与页码直接输入跳转、相邻页后台预取、100%–400% 缩放，以及**深色模式 PDF 夜间智能反色**（自动反色底纸与中性文字，同时保留彩色批注色相与可读性）。
- **页边留白批注区**：可一键展开 PDF 右侧可拖拽调宽的留白区（宽度为原页宽度的 18%–85%），直接在页边进行手写批注。
- **双向联动与框选摘录**：
  - 点击脑图、大纲或详情卡片上的页码徽标，自动进入分屏并定位至对应 PDF 页面，同时叠加绘制原 MarginNote 高亮框与遮罩框。
  - 使用笔盒中的「框选摘录」工具在 PDF 页面上框选区域，自动生成带缩略预览的画布摘录卡片并连线至当前选中节点；点击卡片上的 `P.N ↗` 可随时跳回原文位置并高亮显示。

### 4. 手写笔、压感墨迹与图形规整
- **浮动笔盒工具**：支持浏览与选择、套索（画布）、压感钢笔（4 色 × 细/中/粗 3 档线宽）、荧光笔（4 色，离屏整层合成避免重叠加深）、橡皮（支持**整笔橡皮**与**局部橡皮**两种模式）、框选摘录（PDF / 分屏）以及 20 步撤销/重做历史。
- **图形自动规整**：钢笔绘制后短暂停顿（约 470ms）可自动识别并规整为直线、矩形或椭圆（可在笔盒选项中随时开关）。
- **套索转卡片**：画布上套索圈选手写笔迹后，可一键「转成脑图卡片」（生成独立手写画布卡片）、批量「换色」或「删除」。
- **Android 平板触控笔深度适配**：
  - 原生 Kotlin 桥接拦截触控笔事件，启用 `requestUnbufferedDispatch` 降低书写延迟并完整采集历史采样点与压感值。
  - **防误触（Palm Rejection）**：落笔时自动取消先前的手掌/手指触摸事件，防止手掌倚靠误触画布或卡片。
  - 支持触控笔橡皮端及笔身主/副按键直接切换擦除。
  - 支持设置「触控笔提笔即写」：开启时笔尖落纸即写、手指负责滚动导航；关闭时笔尖优先用于点选卡片与按钮。

### 5. 主题外观、设置与数据导出
- 支持**浅色 / 深色 / 跟随系统**三种外观模式。
- 支持将当前笔记本导出为**带图 Markdown 压缩包（`.zip`）**，包内包含 `README.md` 与 `images/` 附图目录。
- 设置面板提供本地存储占用统计与一键清理渲染缓存。
- 设置面板底部的 **「关于 Folio」** 分组提供开源仓库快捷入口（`github.com/dongyu-will/folio`）与应用内更新检查：
  - **macOS**：打包版本优先通过 `mygo.Updater` 检查并下载安装 Ed25519 签名更新（支持增量补丁与一键重启生效）；若当前版本未启用或尚未发布更新清单，自动回退查询 GitHub Releases。
  - **Android**：通过 GitHub Releases API 检查最新版本，支持下载校验 `SHA256SUMS` 与 HTTPS 安装包并调起系统安装器完成更新（首次安装时自动引导开启未知来源安装权限）。

---

## 平台支持

| 平台 | 定位 | 原生能力与实现方式 |
| --- | --- | --- |
| **Android（平板）** | 主目标平台 | MyGo 原生 UI（自动启用触控大热区布局）+ Kotlin 插件（`internal/androidopen/android/`）：`PdfRenderer` 双队列渲染 PDF、`StylusBridge` 低延迟压感与防误触、`LaunchCover` 主题同步冷启动/恢复无黑屏过渡、Intent 文件关联打开，以及应用内 APK 更新安装（`REQUEST_INSTALL_PACKAGES` + MyGo 内置 `FileProvider`）；导入的 PencilKit 数据由纯 Go 引擎光栅化。 |
| **macOS** | 桌面端支持 | MyGo 原生应用（`.app` / `.dmg`），支持拖拽导入、文件关联与 `mygo.Updater` Ed25519 签名自动更新；通过 `scripts/build-ink.sh` 编译通用二进制（`arm64` + `x86_64`，macOS 12.0+）调用原生 `PDFKit` 与 `PencilKit` 渲染器。 |

> **说明**：PDF 页面渲染目前仅针对 Android 与 macOS 实现了原生后端（见 `pdf_render.go`）；核心笔记存储、导入解析、Markdown 导出及单元测试为纯 Go 实现，可在其他操作系统上直接运行测试。

---

## 快捷键与常用交互

| 操作 | 快捷键 / 交互方式 |
| --- | --- |
| 逐级关闭弹层 / 取消编辑 / 退出当前绘图工具 | `Esc`（Android 使用系统返回键） |
| 保存笔记编辑 / 新建子笔记 | `Cmd/Ctrl + S` |
| 撤销 / 重做手写 | `Cmd/Ctrl + Z` / `Cmd/Ctrl + Shift + Z` |
| 放大 / 缩小（PDF 模式缩放页面；画布 / 分屏模式缩放脑图） | `Cmd/Ctrl + =` / `Cmd/Ctrl + -`，或 `Cmd/Ctrl + 滚轮`、双指捏合 |
| 适应视图 / 重置 PDF 缩放 | `Cmd/Ctrl + 0` |
| 脑图画布缩放至 100%（画布 / 分屏模式，非大纲流） | `Cmd/Ctrl + 1` |
| 选中卡片 / 打开详情模态窗 | 单击卡片选中并显示快捷操作栏；再次点击已选中卡片打开详情 |

---

## 环境要求与快速开始

### 环境要求
- **Go**：`1.27.3` 及以上（见 `go.mod`）
- **MyGo 工具链**：已在 `go.mod` 中通过 `tool github.com/egoist/mygo/cmd/mygo`（`v0.10.0`）声明，可直接使用 `go tool mygo` 调用，无需额外全局安装
- **macOS 构建依赖**：Xcode Command Line Tools（提供 `xcrun swiftc`、`lipo`）
- **Android 构建依赖**：Android SDK / NDK、JDK 与 `adb`

### 检查开发环境
```sh
go tool mygo doctor
```

### 在 macOS 上开发与构建
1. 编译 macOS 原生 PDFKit / PencilKit 辅助渲染器（生成 `resources/darwin/mgnote-ink` 与 `resources/darwin/mgnote-pdf`，已由 `.gitignore` 忽略；`mygo build` 会依据 `mygo.json` 的 `buildCommand` 自动执行，使用 `mygo dev` 前建议手动执行一次）：
   ```sh
   sh scripts/build-ink.sh
   ```
2. 启动开发模式（支持热重载）：
   ```sh
   go tool mygo dev
   ```
3. 打包 macOS 生产版本（在 `build/` 下生成 `.app` 与 `.dmg`）：
   ```sh
   go tool mygo build
   ```
4. 构建并发布带 Ed25519 签名的 macOS 自动更新版本（`mygo.json` 已声明 `updates.publicKey` 与 `updates.github`，私钥保存在仓库外部，通过 `MYGO_UPDATER_PRIVATE_KEY` 传入；仅本地签名构建时去掉 `-upload`）：
   ```sh
   MYGO_UPDATER_PRIVATE_KEY="$(cat /path/to/mygo-update.key)" go tool mygo build -upload
   ```

### 在 Android 上开发与构建
1. 查看已连接的 Android 设备或模拟器：
   ```sh
   go tool mygo devices
   ```
2. 启动 Android 开发调试（可使用 `-device <serial>` 指定设备）：
   ```sh
   go tool mygo dev -platform android
   ```
3. 打包 Android 生产版本（输出 `.apk` 与 Google Play `.aab` 到 `build/android/`）：
   ```sh
   # 仅针对主流 64 位 Android 真机/平板打包单 ABI 精简 APK（arm64-v8a，体积约 20 MB，推荐侧载安装）
   go tool mygo build -platform android/arm64

   # 或构建包含 arm64-v8a / armeabi-v7a / x86_64 的全架构通用 APK 与 Play AAB
   go tool mygo build -platform android
   ```

### 自定义数据目录与命令行导入
可通过环境变量 `MGNOTE_DATA_DIR` 指定本地数据存储目录，也支持在启动可执行文件时通过命令行参数直接导入 `.marginpkg` / `.marginnotes` / `.pdf`：
```sh
# 指定数据目录启动开发模式
MGNOTE_DATA_DIR=/path/to/folio-data go tool mygo dev

# 或运行已构建的二进制并传入待导入文件
MGNOTE_DATA_DIR=/path/to/folio-data /path/to/Folio /path/to/notes.marginpkg
```

---

## 项目目录结构

```text
.
├── .gitignore                       # Git 忽略规则（构建产物、macOS 编译辅助程序、本地笔记等）
├── LICENSE                          # MIT 开源协议
├── README.md                        # 项目说明文档
├── mygo.json                        # MyGo 应用配置、版本号、resourcesExclude、自动更新公钥/仓库、构建钩子与文件关联声明
├── go.mod / go.sum                  # Go 模块（github.com/dongyu-will/folio）依赖与 MyGo tool 声明
├── main.go                          # 应用入口、平台适配初始化、主题背景同步与窗口生命周期
├── workspace.go                     # 工作区状态管理、笔记树构建、搜索、导入/导出、更新状态与编辑逻辑
├── native_view.go                   # 主界面框架、侧边栏、快捷键绑定、笔记详情弹窗与编辑器
├── mindmap.go                       # 脑图无限画布、卡片树/专题框架/精简树布局、缩略图与交互
├── outline_view.go                  # 大纲流视图与标签渲染
├── pdf_view.go                      # PDF 阅读器、分屏拖拽、目录弹窗、留白批注与框选摘录
├── pdf_render.go                    # 跨平台 PDF 渲染调度与夜间智能反色算法
├── ink_view.go                      # 浮动笔盒、离屏双层墨迹缓存、撤销/重做与套索操作
├── ink_geometry.go                  # 压感笔迹几何、图形自动规整、套索命中与局部/整笔擦除
├── android_ink.go                   # Android 触控笔书写区域同步、外观与首帧就绪信号及原生采样桥接
├── drawing_save.go                  # 异步手写落盘队列与变更合并
├── settings_view.go                 # 设置弹窗、偏好项持久化、笔记本管理与「关于 Folio」更新/仓库入口
├── updater.go                       # 跨平台应用更新检查、macOS mygo.Updater 签名更新与 Android APK 校验下载
├── theme.go                         # 浅色/深色主题调色板、MarginNote 16 色卡片与富文本样式映射
├── figma_assets.go                  # //go:embed 嵌入 SVG 图标资源
├── drawing_save_test.go             # 异步手写持久化队列测试
├── updater_test.go                  # 语义化版本比较、GitHub Release 资产筛选与 APK 校验下载测试
├── workspace_test.go                # 笔记树与工作区基础测试
├── workspace_interaction_test.go    # 布局排版、视图切换、设置更新与交互状态测试
├── internal/
│   ├── notebook/
│   │   ├── import.go                # .marginpkg / .marginnotes 只读 SQLite 解析与节点连接
│   │   ├── media.go                 # NSKeyedArchiver bplist 解码、HTML 富文本解析与媒体提取
│   │   ├── ink.go                   # 纯 Go Apple PencilKit (PKDrawing) Protobuf 解析与光栅化
│   │   ├── document.go              # PDF 原文档导入、内容寻址去重与孤立文件 GC
│   │   ├── store.go                 # 分片本地库读写、增量合并更新与原子落盘
│   │   ├── drawing.go               # 手写笔迹与画布卡片的增量日志（Journal）与检查点存储
│   │   ├── settings.go              # 用户偏好设置读写与存储占用统计
│   │   ├── markdown.go              # 带图 Markdown（.zip）导出实现
│   │   ├── drawing_test.go          # 手写增量日志与检查点测试
│   │   ├── media_test.go            # 富文本、bplist、PencilKit 与媒体解析测试
│   │   ├── notebook_test.go         # 导入、增量合并、分片存储与导出测试
│   │   └── workbench_test.go        # 笔记本与 PDF 协同工作流测试
│   └── androidopen/
│       ├── androidopen.go           # Android 原生插件 ID 声明
│       ├── mygo-plugin.json         # Android 插件配置、VIEW/SEND Intent 与 APK 安装权限声明
│       └── android/
│           ├── LaunchCover.kt       # Android 冷启动与前后台恢复主题过渡遮罩（消除 SurfaceView 黑屏闪烁）
│           ├── OpenNotesPlugin.kt   # Android PdfRenderer 渲染、文件 Intent 导入、主题/首帧桥接与安全 APK 安装
│           └── StylusBridge.kt      # Android 压感触控笔采样、低延迟分发与防误触桥接
├── resources/
│   ├── icon.png                     # 应用图标
│   └── figma/                       # 28 个内嵌 UI SVG 图标资源
└── scripts/
    ├── build-ink.sh                 # macOS 通用二进制（arm64 + x86_64）Swift 渲染器编译脚本
    ├── render-ink.swift             # macOS PencilKit (PKDrawing) 转 PNG 辅助程序
    └── render-pdf.swift             # macOS PDFKit 渲染辅助程序
```

---

## 本地存储与技术架构

### 本地数据布局
默认数据目录位于 MyGo 分配的 `PathUserData`（若存在旧版 `<AppData>/MGNote` 目录则自动沿用，也可通过 `MGNOTE_DATA_DIR` 覆盖）：

```text
<data-dir>/
├── index.json                       # 笔记本索引（自动兼容迁移旧版单文件 library.json）
├── settings.json                    # 外观、布局、触控笔等偏好设置
├── books/<sha256>.json              # 每本笔记本独立分片文件
├── drawings/
│   ├── <sha256>.json                # 手写与画布卡片检查点（Checkpoint）
│   └── <sha256>.journal/            # 顺序编号的增量变更日志，达到阈值自动压缩
├── media/<sha256>.(png|jpeg|gif)    # 内容寻址去重的摘录与附图文件（支持自动 GC）
└── documents/<sha256>.pdf           # 内容寻址去重的 PDF 原文档（支持自动 GC）
```

### 技术亮点与核心依赖
- **纯 Go 声明式原生 UI，无 CGO**：基于 `github.com/egoist/mygo` 构建原生渲染界面；通过 `modernc.org/sqlite` 纯 Go 驱动以只读不可变模式（`mode=ro&immutable=1&_pragma=trusted_schema(OFF)`）安全解析 `.marginnotes` 数据库。
- **安全的二进制 Plist 与 HTML 解析**：使用 `howett.net/plist` 解码 MarginNote 数据库内的 Apple `NSKeyedArchiver` 归档（内置递归深度与解码预算限制，不实例化归档类）；使用 `golang.org/x/net/html` 精确提取富文本样式；使用 `github.com/google/uuid` 生成本地新建笔记与画布卡片唯一 ID。
- **分层离屏渲染与增量日志落盘**：荧光笔与钢笔分别维护离屏 `ui.Canvas` 缓存层，荧光笔整层一次性透明合成以防笔迹重叠加深；手写修改经异步队列合并后以顺序增量 Journal 文件原子落盘，降低高频书写 I/O 开销。
- **克制的平台原生层**：仅在系统能力必需处使用少量原生代码——Android 端三个 Kotlin 文件负责 `PdfRenderer`、触控笔桥接、Intent 接收、冷启动/恢复无黑屏过渡与安全 APK 更新安装；macOS 端两个 Swift 脚本负责调用 `PDFKit` 与 `PencilKit`。

---

## 开发与测试

运行全部单元测试与静态检查：

```sh
go test ./...
go vet ./...
go tool mygo vet
```

测试套件涵盖：
- `internal/notebook/`：MarginNote `.marginpkg`/`.marginnotes` 导入解析、富文本与媒体提取、PencilKit 光栅化、增量合并、分片存储、手写 Journal 恢复与 Markdown ZIP 导出。
- 根目录测试（`workspace_test.go`、`workspace_interaction_test.go`、`drawing_save_test.go`、`updater_test.go`）：多布局排版与坐标计算、交互与设置更新流程、异步手写持久化队列以及版本比较/更新校验下载。

---

## 开源协议与贡献

- **开源协议**：本项目采用 MIT License 开源，详见 [LICENSE](LICENSE)。
- **参与贡献**：欢迎通过 Issue 反馈问题或提交 Pull Request。提交代码前请确保 `go test ./...` 与 `go vet ./...` 全部通过。
