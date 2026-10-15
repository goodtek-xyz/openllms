# OSS Quickstart — local only

One gateway on **127.0.0.1:8080**. SQLite + file secrets.

Full copy-paste guide (Docker **and** Go, with expected responses):  
[`docs/oss/README.md`](../../docs/oss/README.md) → published as the public repo root README.

Hosted: [llms.goodtek.xyz](https://llms.goodtek.xyz).

## Docker

```bash
docker compose -f deploy/oss/docker-compose.yml up --build
# background: add -d
curl -sS http://127.0.0.1:8080/health   # {"status":"ok"}
curl -sS http://127.0.0.1:8080/ready    # {"status":"ready"}
curl -sS http://127.0.0.1:8080/livez    # {"status":"ok"}     liveness, answers during start-up too
curl -sS http://127.0.0.1:8080/readyz   # {"status":"ready"}  503 "starting" until start-up finished
```

Wipe volume: `docker compose -f deploy/oss/docker-compose.yml down -v`.  
Corp TLS breaking the image build (`x509: certificate signed by unknown authority`) → use **Go** below.

## Go

```bash
mkdir -p data/secrets bin
go build -o bin/llms-gateway ./cmd/llms-gateway
go build -o bin/llms ./cmd/llms

export HTTP_ADDR=127.0.0.1:8080
export DATABASE_URL=sqlite:./data/llms.db
export LLMS_SECRETS_DIR=./data/secrets
export BOOTSTRAP_TOKEN=local-dev-bootstrap
./bin/llms-gateway
```

Then the same `/health` + `/ready` curls in another terminal.

## Minimal curl path (after gateway is up)

```bash
# bootstrap → save api_key once
curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}'

export LLMS_API_BASE=http://127.0.0.1:8080
export LLMS_API_KEY='sk-gt-…'

# account → route → chat (needs a real upstream key for a live reply)
curl -sS -X POST "$LLMS_API_BASE/control/v1/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"vendor":"openai","name":"main","api_key":"sk-…"}'
# … create route, attach account, then:
curl -sS "$LLMS_API_BASE/r/default/v1/chat/completions" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

## Smoke script

```bash
./deploy/oss/smoke.sh
```

## Data

| Path | Purpose |
|------|---------|
| Docker `/data/llms.db` · Go `./data/llms.db` | SQLite |
| Docker `/data/secrets/` · Go `./data/secrets/` | Secrets |
