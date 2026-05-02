# IPTV Aggregator

自动从 [iptvs.pes.im](https://iptvs.pes.im) 采集全国 IPTV 节点，并发测速、按速度分档，合并订阅源，生成可直接导入播放器的 M3U8 / TXT 播放列表。

---

## 功能

- 从 `iptvs.pes.im` 拉取酒店 IPTV 节点（txiptv / hsmdtv / zhgxtv / jsmpeg）并测速
- 支持自定义订阅源（M3U / TXT 格式），**按服务器分组测速**，不逐条测试
- 速度自动分三档：**高速（≥5 MB/s）/ 普通（1–5 MB/s）/ 低速（0.5–1 MB/s）**，低于 0.5 MB/s 丢弃
- 频道按 **央视 → 卫视 → 其他** 分类，每类再按速度档位排列
- 频道名自动标准化（`CCTV1综合` → `CCTV1` 等）
- 定时自动更新，启动时立即运行一次
- 提供 HTTP 接口，支持手动触发重新扫描
- 更新时间条目放在列表最末尾

---

## 快速开始

### 二进制运行

从 [Releases](../../releases) 下载对应平台的二进制文件：

```bash
# Linux amd64
chmod +x iptv-linux-amd64
./iptv-linux-amd64

# 指定参数
./iptv-linux-amd64 --port 3030 --workers 30 --interval 3h

# 通过环境变量
PORT=3030 WORKERS=30 INTERVAL=3h ./iptv-linux-amd64
```

### Docker 运行

```bash
# 最简运行（使用默认参数）
docker run -d -p 3030:3030 yourname/iptv:latest

# 通过环境变量覆盖参数
docker run -d -p 3030:3030 \
  -e WORKERS=30 \
  -e INTERVAL=3h \
  -e URL1=http://your-subscribe.m3u \
  yourname/iptv:latest
```

### Docker Compose

```yaml
services:
  iptv:
    image: yourname/iptv:latest
    restart: unless-stopped
    ports:
      - "3030:3030"
    environment:
      WORKERS: 30
      INTERVAL: 3h
      URL1: http://your-subscribe.m3u
      URL2: http://another-subscribe.m3u
    volumes:
      - ./data:/app/data  # 可选：持久化缓存文件
```

### 源码编译

```bash
git clone https://github.com/yourname/iptv.git
cd iptv
go mod tidy
go build -o iptv .
./iptv
```

---

## 参数说明

参数优先级：**CLI 参数 > 环境变量 > 默认值**

| CLI 参数 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `--port` | `PORT` | `3030` | HTTP 监听端口 |
| `--workers` | `WORKERS` | `20` | 并发测速协程数 |
| `--top` | `TOP` | `5` | 每种节点类型保留的最优源数量 |
| `--interval` | `INTERVAL` | `6h` | 自动更新间隔（支持 `30m` / `6h` / `24h`） |
| `--url1`~`--url20` | `URL1`~`URL20` | — | 自定义订阅源地址（M3U 或 TXT 格式） |

### 订阅源说明

支持 M3U（`#EXTM3U` 开头）和 TXT（`频道名,URL` 每行一条）两种格式。

订阅源会在每次更新任务开始时**并发下载并缓存到本地**，然后按服务器分组测速：同一个 `host:port` 下的所有频道只测一次，测速结果应用到整组频道，大幅缩短测速时间。

内置默认订阅源：`http://gh-proxy.com/raw.githubusercontent.com/suxuang/myIPTV/main/ipv4.m3u`（下载失败自动跳过）

---

## HTTP 接口

服务启动后监听 `0.0.0.0:<PORT>`：

| 地址 | 说明 |
|---|---|
| `http://ip:3030/` 或 `/iptv` | M3U8 播放列表，填入播放器订阅地址 |
| `http://ip:3030/txt` | TXT 格式播放列表（电视家、TVBox 等使用） |
| `http://ip:3030/status` | 当前状态（JSON），包含运行状态和上次更新时间 |
| `http://ip:3030/retest` | 手动触发重新扫描（POST 或 GET 均可） |

### 播放器填写示例

```
M3U8: http://192.168.1.100:3030/iptv
TXT:  http://192.168.1.100:3030/txt
```

---

## 频道分组

播放列表按以下顺序分组：

```
央视（高速）
央视（普通）
央视（低速）
卫视（高速）
卫视（普通）
卫视（低速）
其他（高速）
其他（普通）
其他（低速）
更新时间
```

---

## 项目结构

```
.
├── main.go          # 入口：路由、cron 调度
├── config.go        # 常量、CLI flags、环境变量读取
├── types.go         # 数据类型定义
├── channel.go       # 频道名清洗、分组、排序
├── speedtest.go     # API 节点测速（txiptv/hsmdtv/jsmpeg/zhgxtv）
├── subscribe.go     # 订阅源下载、解析、按 host 分组测速
├── task.go          # 主任务调度逻辑
├── output.go        # 生成 M3U8 / TXT 文件
├── server.go        # HTTP handler、PID 管理
├── Dockerfile       # 本地构建用（含编译阶段）
└── Dockerfile.prebuilt  # CI 用（直接复制预编译二进制）
```

---

## 速度档位说明

| 档位 | 速度 | group-title 示例 |
|---|---|---|
| 高速 | ≥ 5 MB/s | `央视（高速）` |
| 普通 | 1–5 MB/s | `卫视（普通）` |
| 低速 | 0.5–1 MB/s | `其他（低速）` |
| 丢弃 | < 0.5 MB/s | 不出现在列表中 |

---

## License

MIT
