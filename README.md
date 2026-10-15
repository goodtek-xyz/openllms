# openllms

[![GitHub stars](https://img.shields.io/github/stars/goodtek-xyz/openllms?style=social)](https://github.com/goodtek-xyz/openllms)

Turn ChatGPT / Claude (and Codex) seats you already pay for into **one OpenAI-compatible API**.  
The gateway watches remaining quota and spreads traffic so one login doesn’t get burned while others sit idle.

Self-host is **free forever** under MIT — the gateway itself has no usage meter. If this helps, [★ Star the repo](https://github.com/goodtek-xyz/openllms).

[한국어](README.ko.md) · [日本語](README.ja.md) · [中文](README.zh.md) · [MIT](LICENSE) · [Hosted cloud](https://llms.goodtek.xyz)

---

## What you get

- **One Base URL** for Cursor, Claude Code, Codex, scripts
- **Several accounts on one route** — failover when a seat is empty, cooling, or errors
- **Quota-aware routing** — leftover allowance and reset timing via `llms status`
- **Local-first OSS** — Docker *or* a local Go binary + SQLite + files on disk

This repo is the open engine behind [llms](https://llms.goodtek.xyz) (hosted by [goodtek](https://goodtek.xyz)). Hosted billing UI is **not** here.

---

## Pick a start path

| | **A. Docker** | **B. Go** |
|--|---------------|-----------|
| Need | Docker Desktop / Engine + Compose | Go toolchain (`go.mod` → currently **1.24**) |
| Best when | You want one command | Corp TLS breaks `go mod` inside Docker builds |
| Data | Docker volume `oss-data` | `./data/llms.db` + `./data/secrets/` |
| Listen | `127.0.0.1:8080` | `127.0.0.1:8080` (you set it) |

Local bootstrap token used everywhere below: **`local-dev-bootstrap`**  
(local only — do not put this on a public interface)

---

## 1. Install & start

### A. Docker

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms

# Foreground — logs stay in this terminal (Ctrl+C stops)
docker compose -f deploy/oss/docker-compose.yml up --build
```

Or background:

```bash
docker compose -f deploy/oss/docker-compose.yml up --build -d
docker compose -f deploy/oss/docker-compose.yml ps
docker compose -f deploy/oss/docker-compose.yml logs -f gateway
```

Healthy start looks like:

```text
llms-gateway listening ... addr=0.0.0.0:8080
```

(or similar `listening` / `database backend` lines)

Stop / wipe:

```bash
docker compose -f deploy/oss/docker-compose.yml down      # keep DB volume
docker compose -f deploy/oss/docker-compose.yml down -v   # delete SQLite + secrets too
```

If the **image build** fails with:

```text
x509: certificate signed by unknown authority
```

your network is intercepting TLS (common on corp laptops). Use **B. Go**, or inject your org root CA into the build.

### B. Go (no Docker)

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms
mkdir -p data/secrets bin

go build -o bin/llms-gateway ./cmd/llms-gateway
go build -o bin/llms ./cmd/llms
```

Windows Git Bash may produce `bin/llms-gateway.exe` / `bin/llms.exe` — same idea.

**Terminal 1 — leave running:**

```bash
export HTTP_ADDR=127.0.0.1:8080
export DATABASE_URL=sqlite:./data/llms.db
export LLMS_SECRETS_DIR=./data/secrets
export BOOTSTRAP_TOKEN=local-dev-bootstrap

./bin/llms-gateway
```

| Env | Meaning |
|-----|---------|
| `HTTP_ADDR` | Listen address (`127.0.0.1:8080` = this machine only) |
| `DATABASE_URL` | SQLite file, e.g. `sqlite:./data/llms.db` |
| `LLMS_SECRETS_DIR` | Secret files directory |
| `BOOTSTRAP_TOKEN` | Must match `X-Bootstrap-Token` on first setup |

Don’t commit `./data/`.

Prefer Postgres? `make dev-db` starts one in Docker on `127.0.0.1:54329` (`deploy/dev/compose.yaml`); then
`export DATABASE_URL=postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable` before `./bin/llms-gateway`.
`make dev-db-down` stops it; the data stays in a Docker volume. For local development only.

---

## 2. Smoke check (before anything else)

**Terminal 2** (gateway still up):

```bash
curl -sS http://127.0.0.1:8080/health
curl -sS http://127.0.0.1:8080/ready
```

Expected (shape may add fields, but status is what matters):

```json
{"status":"ok"}
```

```json
{"status":"ready"}
```

If `curl: Failed to connect` → gateway isn’t running (start §1 again; don’t close that terminal).

The gateway listens before migrations and the database connection are done. Until then `/health` and `/livez` already answer 200, `/readyz` answers 503 `{"status":"starting"}` (200 `{"status":"ready"}` afterwards) and other paths answer 503 `{"error":"starting"}`. For orchestrator probes use `/livez` (liveness) and `/readyz` (readiness; no database ping, so a database switchover does not take every replica out at once). `/ready` still pings the database for monitoring.

---

## 3. First end-to-end test (curl)

This path uses an **OpenAI-compatible API key** vendor so you can verify without OAuth.  
Replace `sk-…` with a real upstream key when you want a live model reply.

### 3.1 Bootstrap → gateway API key

```bash
curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}'
```

Example response:

```json
{
  "api_key": "sk-gt-…",
  "warning": "store api_key now; it will not be shown again"
}
```

Save it (shown once):

```bash
export LLMS_API_BASE=http://127.0.0.1:8080
export LLMS_API_KEY='sk-gt-…'   # paste the value from JSON
```

Optional pretty extract if you have `jq`:

```bash
BOOT=$(curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}')
echo "$BOOT" | jq .
export LLMS_API_KEY="$(echo "$BOOT" | jq -r .api_key)"
export LLMS_API_BASE=http://127.0.0.1:8080
```

> Re-running bootstrap against an already-initialized DB may fail — wipe the volume / delete `./data` if you need a clean slate.

### 3.2 Add an upstream account

```bash
curl -sS -X POST "$LLMS_API_BASE/control/v1/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"vendor":"openai","name":"main","api_key":"sk-…"}'
```

Example response includes an account `id` (UUID). Save it:

```bash
export ACCOUNT_ID='…'   # from JSON "id"
```

### 3.3 Create a route and attach the account

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

Public OpenAI-compatible surface for this route:

```text
http://127.0.0.1:8080/r/default/v1
```

### 3.4 Chat completions test

```bash
curl -sS "$LLMS_API_BASE/r/default/v1/chat/completions" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

Success looks like a normal OpenAI chat payload (`choices[0].message.content`, etc.).  
Upstream auth errors mean the vendor key / account is wrong — gateway health can still be OK.

Change routing later:

```bash
# after CLI is built / env is set — see §4
llms route update default --preset quota-first
```

---

## 4. Same flow with the CLI

With the gateway still running and `LLMS_API_BASE` / `LLMS_API_KEY` set:

```bash
# if you used Docker only, build the CLI once:
go build -o bin/llms ./cmd/llms

./bin/llms status
./bin/llms status --refresh

./bin/llms add
# prompts: OAuth (ChatGPT / Claude / Codex) or paste an API key

./bin/llms route create
# wizard: slug, accounts, preset (default leans quota-first)

./bin/llms route list
./bin/llms route url default
./bin/llms models default
./bin/llms env default
```

Useful day-to-day:

```bash
./bin/llms route update default --preset reset-soon
./bin/llms route key default      # route-scoped sk-gt (shown once)
./bin/llms disconnect openai:main
./bin/llms key list
./bin/llms credentials
```

| Preset (`--preset`) | Behavior |
|---------------------|----------|
| `failover` | Try accounts in order until one works |
| `fill-first` | Burn the first healthy seat, then the next |
| `balance` | Round-robin among healthy seats |
| `prefer-primary` | Soft preference for primary (~80/20) |
| `quota-first` | Prefer more remaining quota (earlier reset on ties) |
| `reset-soon` | Prefer seats whose window resets sooner |
| `steward` | Score remaining × reset urgency (leans OAuth) |
| `parallel` | Fan-out when enabled for your build/plan |

Hosted helpers (`llms login`, `llms plan`, `llms free`, `llms upgrade`) talk to [llms.goodtek.xyz](https://llms.goodtek.xyz). Local OSS can ignore them after bootstrap.

---

## 5. Point Cursor / an agent at the gateway

| Setting | Value |
|---------|--------|
| Base URL | `http://127.0.0.1:8080/r/default/v1` |
| API key | your `sk-gt-…` (or a route key from `llms route key default`) |
| Model | one id from `llms models default` |

Same shape as OpenAI Chat Completions — most tools only need Base URL + key.

---

## 6. Automated smoke (optional)

```bash
./deploy/oss/smoke.sh
go test ./internal/httpserver/ -run TestOSSE2E -count=1 -v
```

`smoke.sh` boots a temporary gateway on another port and checks `/health` + `/ready` (no Docker required).

---

## 7. Data & backup

| Path | Purpose |
|------|---------|
| Docker: volume `/data/llms.db` · Go: `./data/llms.db` | SQLite |
| Docker: `/data/secrets/` · Go: `./data/secrets/` | Secret files |

Back up both if you care about restore. Don’t commit them.

---

## 8. When something breaks

| Symptom | Fix |
|---------|-----|
| `Failed to connect` on `:8080` | Gateway not running; start §1 and leave it up |
| `health`/`ready` not ok | Port busy? Logs: `docker compose … logs -f gateway` or gateway stdout |
| `401` | Wrong/missing `LLMS_API_KEY`; wipe data and re-bootstrap |
| `bootstrap` rejected | Header must be `X-Bootstrap-Token: local-dev-bootstrap` |
| Docker build `x509 … unknown authority` | Corp TLS → use **Go** path |
| `sqlite … unable to open` / `out of memory (14)` | Volume perms. `down -v` then `up --build` (Docker) or fix `./data` ownership (Go) |
| Empty models / upstream errors | Re-run `llms add` / check vendor key; `llms status --refresh` |
| Editor can’t connect | Base URL must include `/r/<slug>/v1` |
| Only localhost works | Sample binds `127.0.0.1` on purpose |

More compose notes: [`deploy/oss/README.md`](deploy/oss/README.md).

---

## Repo map

| Path | Role |
|------|------|
| `cmd/llms-gateway` | HTTP gateway |
| `cmd/llms` | CLI |
| `internal/` | Routing, quota, vendors, store |
| `deploy/oss/` | Local Docker sample + smoke script |

---

## What this is not

- Not a token marketplace (you use **your** seats)
- Not a “fake official CLI” / cloaking proxy
- Not “unlimited ChatGPT API” marketing
- Not the hosted admin/billing UI (cloud-only)

---

## License

[MIT](LICENSE).

---

## From goodtek

[goodtek](https://goodtek.xyz) builds practical AI products with a bias toward **trust, safety, and operable systems** — clear boundaries, honest ops, and tools you can run in production without guessing.

| | |
|---|---|
| **goodtek** | Trusted product studio — [goodtek.xyz](https://goodtek.xyz) |
| **openllms / llms** | Subscription → API gateway (this project) — hosted [llms.goodtek.xyz](https://llms.goodtek.xyz) |
| **vibePulse** | Always-on monitoring for sites, APIs, and agents — [vibepulse.goodtek.xyz](https://vibepulse.goodtek.xyz) |
| **VibeCrew** | Vibe-coding builder community — [vibecrew.kr](https://vibecrew.kr) |

Contact: [hello@goodtek.xyz](mailto:hello@goodtek.xyz) · X [@goodtek_xyz](https://x.com/goodtek_xyz) · Threads [@goodtek.xyz](https://www.threads.net/@goodtek.xyz)
