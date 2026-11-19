# Folio

基于 Go 与 [MyGo](https://github.com/egoist/mygo) 构建的离线优先（Local-first）MarginNote 笔记阅读、PDF 批注与思维导图工作台，支持 Android 平板/手机与 macOS。

## 下载安装

前往 [GitHub Releases](https://github.com/dongyu-will/folio/releases) 下载最新版本：
- **Android（手机 / 平板）**：下载 `arm64` APK 安装包直接安装。
- **macOS（12.0+）**：下载 `.dmg` 镜像并拖入「应用程序」。
- **应用内自动更新**：应用启动后会自动节流检查新版本并提示，也可在「设置 → 关于 Folio」中手动检查与下载更新。

## 核心特性

- **笔记导入与本地编辑**：支持 `.marginpkg` / `.marginnotes` / `.pdf` 导入，跨平台还原富文本、摘录图与 Apple PencilKit 手写，支持本地编辑与增量合并。
- **4 种脑图与大纲视图**：提供卡片树、专题框架、大纲流、精简树四种形态，支持全文搜索、缩略图导航与卡片—PDF 双向跳页。
- **PDF 分屏与留白批注**：支持横/竖屏自适应分屏、目录跳转、深色模式夜间智能反色、可调页边留白批注与框选摘录。
- **压感手写与触控笔适配**：内置压感钢笔、荧光笔、整笔/局部橡皮、套索转卡片与图形自动规整，支持 Android 触控笔低延迟采样与防误触。
- **主题、导出与更新**：提供浅色 / 深色 / 跟随系统主题、带图 Markdown（`.zip`）导出，以及应用内自动更新检查与安装。

## 平台支持

| 平台 | 实现方式与原生能力 |
| --- | --- |
| **Android（平板 / 手机）** | MyGo 原生 UI + Kotlin 插件（`PdfRenderer` 渲染、触控笔压感与防误触、Intent 打开/分享导入、应用内 APK 安装），PencilKit 手写由纯 Go 引擎光栅化 |
| **macOS（12.0+）** | MyGo 原生应用（`.app` / `.dmg`），支持拖拽与文件关联导入，通过原生 `PDFKit` / `PencilKit` 渲染器与 `mygo.Updater` 检查更新 |

## 快速开始

### 环境要求

- **Go 1.27.3+**（`go.mod` 已声明 `mygo` 工具链，可直接运行 `go tool mygo`）
- **macOS**：Xcode Command Line Tools（`xcrun swiftc`、`lipo`）
- **Android**：Android SDK / NDK、JDK 与 `adb`

```sh
go tool mygo doctor
```

### macOS 开发与构建

```sh
# 首次 dev 前先编译原生 PDFKit / PencilKit 渲染器（build 会自动执行）
sh scripts/build-ink.sh

go tool mygo dev      # 开发模式（热重载）
go tool mygo build    # 打包 .app 与 .dmg（输出至 build/）
```

### Android 开发与构建

```sh
go tool mygo devices
go tool mygo dev -platform android

# 构建 64 位精简 APK（arm64-v8a，推荐真机侧载）
go tool mygo build -platform android/arm64

# 构建全架构通用 APK 与 Play AAB
go tool mygo build -platform android
```

> 可通过环境变量 `MGNOTE_DATA_DIR` 指定本地数据目录，也支持启动可执行文件时以命令行参数直接导入笔记或 PDF。

### 运行测试

```sh
go test ./...
go vet ./...
```

## 核心目录

```text
.
├── *.go                  # 应用入口、脑图/大纲/PDF 视图、手写墨迹与更新逻辑
├── internal/notebook/    # .marginpkg/.marginnotes 解析、PencilKit 光栅化、分片存储与导出
├── internal/androidopen/ # Android 原生插件（PdfRenderer、触控笔桥接、Intent 与启动遮罩）
├── scripts/              # macOS PDFKit / PencilKit Swift 渲染器与编译脚本
├── resources/            # 应用图标与内嵌 SVG 图标资源
└── mygo.json             # MyGo 应用配置、文件关联与自动更新配置
```

## 开源协议

本项目基于 [MIT License](LICENSE) 开源。
