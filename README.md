# m3u8-recorder

一个基于 Go + FFmpeg 的多路 `m3u8` 录制工具，带有简易 Web UI，支持：

- 多路并发录制
- 单路启动/停止、全部停止
- 实时状态（时长、速度、分片数、累计流量）
- 可选全局定时录制窗口（例如 `08:00` 到次日 `02:00`）
- 按直播 URL 自动归类本地目录

---

## 1. 功能概览

- 录制后端使用 `ffmpeg`，按 `SPLIT_SECONDS` 分片输出 `.ts` 文件。
- 每个直播间单独目录保存录像（目录名由 URL basename 推导并清洗）。
- 文件名带启动时间戳前缀（如 `20260228_010500_000001.ts`）。
- Web UI 支持：
  - 添加房间（仅登记，不立即录制）
  - 单路启动
  - 单路停止/启动
  - 全部停止
  - 查看原始 JSON 状态

---

## 2. 环境要求

- Go 1.22+（建议）
- 系统可执行 `ffmpeg`（确保在 `PATH` 中）

可用命令验证：

```bash
go version
ffmpeg -version
```

---

## 3. 快速开始

### 3.1 安装依赖并启动

```bash
go mod download
go run .
```

默认 Web UI 地址：

- `http://localhost:8080`

### 3.2 使用示例配置

仓库已提供示例配置文件：`.env.example`。

```bash
cp .env.example .env
# 按需修改 .env
```

程序会自动加载 `.env`（若存在）。

---

## 4. 配置项

| 变量名 | 默认值 | 说明 |
|---|---|---|
| `DOWNLOAD_PATH` | `./downloads` | 录像根目录 |
| `SPLIT_SECONDS` | `600` | ffmpeg segment 切片秒数 |
| `WEBUI_PORT` | `8080` | Web UI 监听端口 |
| `REQUEST_UA` | Chrome UA | 拉流请求 User-Agent |
| `RECORD_SCHEDULE_START` | 空 | 定时录制开始时间（`HH:MM`） |
| `RECORD_SCHEDULE_END` | 空 | 定时录制结束时间（`HH:MM`） |

### 定时录制说明

- 只有 `RECORD_SCHEDULE_START` 和 `RECORD_SCHEDULE_END` **都设置**时才启用。
- 支持跨天窗口：
  - 例如 `08:00` 到 `02:00`，表示每天早上 8 点开始录，持续到次日凌晨 2 点。
- 在时间窗外：运行中的房间会自动停止。
- 在时间窗内：处于 idle 的房间会自动启动。

---

## 5. Web API

### `POST /add`

添加房间（只添加到列表，不会立即开始录制）。

请求体：

```json
{ "url": "https://example.com/live/index.m3u8" }
```

### `POST /start`

启动已添加的直播间录制。

请求体：

```json
{ "url": "https://example.com/live/index.m3u8" }
```

### `POST /stop`

- 传 `url`：停止指定直播间
- 不传 body：停止全部正在录制的直播间

请求体（单路停止）：

```json
{ "url": "https://example.com/live/index.m3u8" }
```

### `GET /status`

返回全局状态和房间状态列表。

示例字段（节选）：

```json
{
  "download_root": "./downloads",
  "split_seconds": 600,
  "active_count": 1,
  "schedule_enabled": true,
  "schedule_start": "08:00",
  "schedule_end": "02:00",
  "schedule_in_range": true,
  "rooms": [
    {
      "url": "https://example.com/live/index.m3u8",
      "state": "running",
      "uptime_seconds": 120,
      "speed_kb_per_s": 512.3,
      "segments_done": 12,
      "bytes_done": 123456789,
      "can_start": false,
      "can_stop": true
    }
  ]
}
```

---

## 6. 本地存储结构

示例：

```text
downloads/
└── index.m3u8/
    ├── 20260228_010500_000001.ts
    ├── 20260228_010500_000002.ts
    └── ...
```

说明：

- 目录名优先使用 URL path 的 basename（并清洗非法字符）。
- basename 不可用时回退为 host/hash。

---

## 7. 常见问题

### Q1：速度显示为 0？

- 刚启动时统计样本不足，1~2 秒后会稳定。
- 停止后速度会归零（预期行为）。

### Q2：为什么“已停止”还能启动？

- 这是设计行为，方便单路房间快速恢复录制。

### Q3：定时录制不生效？

- 检查是否同时设置了 `RECORD_SCHEDULE_START` 与 `RECORD_SCHEDULE_END`。
- 检查时间格式是否为 `HH:MM`（24 小时制）。

---

## 8. 开发与检查

```bash
go test ./...
go vet ./...
```
