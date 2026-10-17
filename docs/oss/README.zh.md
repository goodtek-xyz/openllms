# openllms

[![GitHub stars](https://img.shields.io/github/stars/goodtek-xyz/openllms?style=social)](https://github.com/goodtek-xyz/openllms)

把你已经在付的 ChatGPT / Claude（以及 Codex）席位，收成 **一个 OpenAI 兼容 API**。  
按剩余额度分流量，避免一边烧光、一边闲着。

自托管按 MIT **永久免费**。网关本身没有用量计费。觉得有用就 [★ Star](https://github.com/goodtek-xyz/openllms)。

[English](README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [MIT](LICENSE) · [托管版](https://llms.goodtek.xyz)

---

## 你能得到什么

- 给 Cursor、Claude Code、Codex、脚本用的 **一个 Base URL**
- **一条路由挂多个账号** — 额度空、冷却、出错就切下一个
- **感知配额的路由** — 用 `llms status` 看剩余与重置时间
- **本地优先的 OSS** — Docker *或* Go 二进制 + SQLite + 磁盘文件

本仓库是 [llms](https://llms.goodtek.xyz) 的开源引擎（由 [goodtek](https://goodtek.xyz) 维护）。托管版的计费界面不在这里。

---

## 选一条启动路径

| | **A. Docker** | **B. Go** |
|--|---------------|-----------|
| 需要 | Docker Desktop / Engine + Compose | Go 工具链（`go.mod` → 当前 **1.27**） |
| 适合 | 想一条命令搞定 | 公司网 TLS 导致 Docker 内 `go mod` 失败 |
| 数据 | Docker 卷 `oss-data` | `./data/llms.db` + `./data/secrets/` |
| 监听 | `127.0.0.1:8080` | `127.0.0.1:8080`（自己配置） |

下面示例用的本地引导令牌：**`local-dev-bootstrap`**  
（仅本地 — 不要暴露到公网）

---

## 1. 安装与启动

### A. Docker

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms

# 前台 — 日志在这个终端（Ctrl+C 停止）
docker compose -f deploy/oss/docker-compose.yml up --build
```

后台：

```bash
docker compose -f deploy/oss/docker-compose.yml up --build -d
docker compose -f deploy/oss/docker-compose.yml ps
docker compose -f deploy/oss/docker-compose.yml logs -f gateway
```

正常启动日志示例：

```text
llms-gateway listening ... addr=0.0.0.0:8080
```

（出现 `listening` / `database backend` 一类即可）

停止 / 清数据：

```bash
docker compose -f deploy/oss/docker-compose.yml down      # 保留 DB 卷
docker compose -f deploy/oss/docker-compose.yml down -v   # 连同 SQLite、密钥一起删
```

若 **镜像构建** 失败并出现：

```text
x509: certificate signed by unknown authority
```

多半是公司网络在做 TLS 拦截。改用 **B. Go**，或把组织 Root CA 打进构建。

### B. Go（不用 Docker）

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms
mkdir -p data/secrets bin

go build -o bin/llms-gateway ./cmd/llms-gateway
go build -o bin/llms ./cmd/llms
```

Windows Git Bash 下可能是 `bin/llms-gateway.exe` / `bin/llms.exe`。

**终端 1 — 不要关：**

```bash
export HTTP_ADDR=127.0.0.1:8080
export DATABASE_URL=sqlite:./data/llms.db
export LLMS_SECRETS_DIR=./data/secrets
export BOOTSTRAP_TOKEN=local-dev-bootstrap

./bin/llms-gateway
```

| 环境变量 | 含义 |
|----------|------|
| `HTTP_ADDR` | 监听地址（`127.0.0.1:8080` = 仅本机） |
| `DATABASE_URL` | SQLite 文件，例如 `sqlite:./data/llms.db` |
| `LLMS_SECRETS_DIR` | 密钥目录 |
| `BOOTSTRAP_TOKEN` | 须与首次设置的 `X-Bootstrap-Token` 一致 |

不要提交 `./data/`。

想用 Postgres？`make dev-db` 会在 Docker 中启动 `127.0.0.1:54329` 上的 Postgres（`deploy/dev/compose.yaml`），
然后在 `./bin/llms-gateway` 之前设置 `export DATABASE_URL=postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable`。
`make dev-db-down` 停止，数据保留在 Docker 卷中。仅用于本地开发。

---

## 2. 冒烟检查（做别的事之前）

**终端 2**（网关保持运行）：

```bash
curl -sS http://127.0.0.1:8080/health
curl -sS http://127.0.0.1:8080/ready
```

期望（字段可能更多，关键看 `status`）：

```json
{"status":"ok"}
```

```json
{"status":"ready"}
```

`curl: Failed to connect` → 网关没起来（重做 §1，并保持该终端）。

网关会在迁移和数据库连接完成之前先开始监听。在此期间 `/health` 和 `/livez` 立即返回 200，`/readyz` 返回 503 `{"status":"starting"}`（启动完成后返回 200 `{"status":"ready"}`），其他路径返回 503 `{"error":"starting"}`。编排器的 probe 请用 `/livez`（liveness）和 `/readyz`（readiness）。`/readyz` 不直接检查数据库，因此数据库切换期间不会让所有副本同时下线。用于监控的 `/ready` 仍然检查数据库。

---

## 3. 第一次端到端测试（curl）

下面用 **OpenAI 兼容 API key** 厂商，不走 OAuth 也能验通。  
若要真实模型回复，把 `sk-…` 换成真实上游 key。

### 3.1 引导 → 网关 API key

```bash
curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}'
```

响应示例：

```json
{
  "api_key": "sk-gt-…",
  "warning": "store api_key now; it will not be shown again"
}
```

只显示一次，立刻保存：

```bash
export LLMS_API_BASE=http://127.0.0.1:8080
export LLMS_API_KEY='sk-gt-…'   # 粘贴 JSON 里的值
```

有 `jq` 时：

```bash
BOOT=$(curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}')
echo "$BOOT" | jq .
export LLMS_API_KEY="$(echo "$BOOT" | jq -r .api_key)"
export LLMS_API_BASE=http://127.0.0.1:8080
```

> 对已初始化的 DB 再跑引导可能失败。要干净重来：删卷 / 删 `./data` 后重启。

### 3.2 添加上游账号

```bash
curl -sS -X POST "$LLMS_API_BASE/control/v1/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"vendor":"openai","name":"main","api_key":"sk-…"}'
```

保存返回的账号 `id`（UUID）：

```bash
export ACCOUNT_ID='…'   # JSON "id"
```

### 3.3 创建路由并挂上账号

```bash
curl -sS -X POST "$LLMS_API_BASE/control/v1/routes" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"slug":"default","strategy":"sequential","default_model":"gpt-4o-mini"}'

curl -sS -X POST "$LLMS_API_BASE/control/v1/routes/default/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d "{\"account_id\":\"$ACCOUNT_ID\",\"position\":0,\"weight\":1}"
```

该路由对外的 OpenAI 兼容地址：

```text
http://127.0.0.1:8080/r/default/v1
```

### 3.4 chat completions 测试

```bash
curl -sS "$LLMS_API_BASE/r/default/v1/chat/completions" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

成功时是标准 OpenAI chat 形态（`choices[0].message.content` 等）。  
上游鉴权错误是厂商 key/账号问题 — 网关 health 仍可能是 OK。

改路由策略：

```bash
llms route update default --preset quota-first
```

---

## 4. 用 CLI 走同样流程

网关已运行，且设置了 `LLMS_API_BASE` / `LLMS_API_KEY`：

```bash
# 若只用了 Docker，先编译一次 CLI
go build -o bin/llms ./cmd/llms

./bin/llms status
./bin/llms status --refresh

./bin/llms add
# 提示：OAuth（ChatGPT / Claude / Codex）或粘贴 API key

./bin/llms route create
# 向导：slug、账号、预设（默认偏 quota-first）

./bin/llms route list
./bin/llms route url default
./bin/llms models default
./bin/llms env default
```

日常：

```bash
./bin/llms route update default --preset reset-soon
./bin/llms route key default      # 路由级 sk-gt（只显示一次）
./bin/llms disconnect openai:main
./bin/llms key list
./bin/llms credentials
```

| 预设（`--preset`） | 行为 |
|--------------------|------|
| `failover` | 按顺序试到成功 |
| `fill-first` | 先用尽第一个健康席位，再下一个 |
| `balance` | 健康席位轮询 |
| `prefer-primary` | 软偏好主账号（约 80/20） |
| `quota-first` | 优先剩余更多的（相同时选更快重置的） |
| `reset-soon` | 优先窗口即将重置的席位 |
| `steward` | 剩余 × 重置紧迫度（偏 OAuth） |
| `parallel` | 构建/套餐启用时扇出 |

预构建二进制：[GitHub Releases](https://github.com/goodtek-xyz/openllms/releases) — 每个版本都附带 `SHA256SUMS`
和构建来源证明（`gh attestation verify llms_<os>_<arch> --repo goodtek-xyz/openllms`）。

更新 CLI：`llms update` 用网关在 `/dist` 提供的构建原地替换正在运行的二进制
（用 `/dist/SHA256SUMS` 校验，任何一步失败都保留原二进制）；如果 PATH 里先找到另一个 `llms`，会给出提示。
`/dist` 由以 `CLI_DIST=1` 构建的网关镜像提供；没有时用 `go build -o bin/llms ./cmd/llms`
（或 `go install github.com/goodtek-xyz/openllms/cmd/llms@latest`）重新构建。

托管相关（`llms login`、`llms plan`、`llms free`、`llms upgrade`）面向 [llms.goodtek.xyz](https://llms.goodtek.xyz)。本地 OSS 引导后可忽略。

---

## 5. 把 Cursor / Agent 指到网关

| 设置 | 值 |
|------|-----|
| Base URL | `http://127.0.0.1:8080/r/default/v1` |
| API key | `sk-gt-…`（或 `llms route key default`） |
| Model | 来自 `llms models default` |

形态与 OpenAI Chat Completions 相同，多数工具只需 Base URL + key。

---

## 6. 自动冒烟（可选）

```bash
./deploy/oss/smoke.sh
go test ./internal/httpserver/ -run TestOSSE2E -count=1 -v
```

`smoke.sh` 会在另一端口临时起网关并检查 `/health`、`/ready`（不需要 Docker）。

---

## 7. 数据与备份

| 路径 | 用途 |
|------|------|
| Docker：卷 `/data/llms.db` · Go：`./data/llms.db` | SQLite |
| Docker：`/data/secrets/` · Go：`./data/secrets/` | 密钥文件 |

需要恢复就两边一起备份。不要提交进仓库。

---

## 8. 出问题时

| 现象 | 处理 |
|------|------|
| `:8080` `Failed to connect` | 网关未启动 — 重做 §1，并保持终端 |
| `health`/`ready` 异常 | 端口占用？看日志：`docker compose … logs -f gateway` 或 stdout |
| `401` | `LLMS_API_KEY` 错/缺；清数据后重新引导 |
| `bootstrap` 被拒 | 头必须是 `X-Bootstrap-Token: local-dev-bootstrap` |
| Docker 构建 `x509 … unknown authority` | 公司 TLS → 走 **Go** |
| `sqlite … unable to open` / `out of memory (14)` | 卷权限。Docker：`down -v` 后再 `up --build`；Go：检查 `./data` 属主 |
| 模型为空 / 上游错误 | 重跑 `llms add`；核对厂商 key；`llms status --refresh` |
| 编辑器连不上 | Base URL 必须含 `/r/<slug>/v1` |
| 只能本机访问 | 示例故意只绑 `127.0.0.1` |

更多 compose 说明：[`deploy/oss/README.md`](deploy/oss/README.md)。

---

## 仓库地图

| 路径 | 作用 |
|------|------|
| `cmd/llms-gateway` | HTTP 网关 |
| `cmd/llms` | CLI |
| `internal/` | 路由、配额、厂商、存储 |
| `deploy/oss/` | 本地 Docker 示例 + 冒烟脚本 |

---

## 这不是什么

- 不是代币市场（用的是**你自己的**席位）
- 不是「假官方 CLI」/ 伪装代理
- 不是「无限 ChatGPT API」话术
- 不是托管版管理/计费 UI（仅云端）

---

## 许可

[MIT](LICENSE).

---

## 来自 goodtek

[goodtek](https://goodtek.xyz) 是一家偏重 **可信、安全、可运营系统** 的 AI 产品工作室：边界清晰、表述诚实、工具能直接上生产环境。

| | |
|---|---|
| **goodtek** | 可信赖的产品工作室 — [goodtek.xyz](https://goodtek.xyz) |
| **openllms / llms** | 订阅 → API 网关（本项目） — 托管 [llms.goodtek.xyz](https://llms.goodtek.xyz) |
| **vibePulse** | 站点 / API / Agent 持续监控 — [vibepulse.goodtek.xyz](https://vibepulse.goodtek.xyz) |
| **VibeCrew** | Vibe coding 构建者社区 — [vibecrew.kr](https://vibecrew.kr) |

联系：[hello@goodtek.xyz](mailto:hello@goodtek.xyz) · X [@goodtek_xyz](https://x.com/goodtek_xyz) · Threads [@goodtek.xyz](https://www.threads.net/@goodtek.xyz)
