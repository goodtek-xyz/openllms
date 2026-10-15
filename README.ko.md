# openllms

[![GitHub stars](https://img.shields.io/github/stars/goodtek-xyz/openllms?style=social)](https://github.com/goodtek-xyz/openllms)

이미 내고 있는 ChatGPT / Claude(그리고 Codex) 좌석을 **OpenAI 호환 API 하나**로 묶습니다.  
남은 한도를 보고 트래픽을 나눠서, 한쪽만 태우고 나머지는 노는 일을 줄입니다.

셀프호스트는 MIT로 **영구 무료**입니다. 게이트웨이 자체에 사용량 미터는 없습니다. 도움이 됐다면 [★ Star](https://github.com/goodtek-xyz/openllms).

[English](README.md) · [日本語](README.ja.md) · [中文](README.zh.md) · [MIT](LICENSE) · [호스팅](https://llms.goodtek.xyz)

---

## 뭐가 되나

- Cursor, Claude Code, Codex, 스크립트용 **Base URL 하나**
- 한 라우트에 **계정 여러 개** — 한도·쿨링·오류 나면 다음으로
- **쿼터 인식 라우팅** — `llms status`로 남은 한도·리셋 시점
- **로컬 우선 OSS** — Docker *또는* Go 바이너리 + SQLite + 디스크 파일

이 저장소는 [llms](https://llms.goodtek.xyz)의 오픈 엔진입니다 ([goodtek](https://goodtek.xyz)이 만듭니다). 호스팅 결제 UI는 여기 없습니다.

---

## 시작 경로 고르기

| | **A. Docker** | **B. Go** |
|--|---------------|-----------|
| 필요 | Docker Desktop / Engine + Compose | Go 툴체인 (`go.mod` → 현재 **1.24**) |
| 이럴 때 | 명령 한 줄로 끝내고 싶을 때 | 회사망 TLS 때문에 Docker 안 `go mod`가 깨질 때 |
| 데이터 | Docker 볼륨 `oss-data` | `./data/llms.db` + `./data/secrets/` |
| 리슨 | `127.0.0.1:8080` | `127.0.0.1:8080` (직접 설정) |

아래 예시에서 쓰는 로컬 부트스트랩 토큰: **`local-dev-bootstrap`**  
(로컬 전용 — 공개 인터페이스에 올리지 마세요)

---

## 1. 설치 & 기동

### A. Docker

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms

# 포그라운드 — 이 터미널에 로그 (Ctrl+C면 중지)
docker compose -f deploy/oss/docker-compose.yml up --build
```

백그라운드:

```bash
docker compose -f deploy/oss/docker-compose.yml up --build -d
docker compose -f deploy/oss/docker-compose.yml ps
docker compose -f deploy/oss/docker-compose.yml logs -f gateway
```

정상 기동 로그 예:

```text
llms-gateway listening ... addr=0.0.0.0:8080
```

(`listening` / `database backend` 비슷한 줄이면 OK)

중지 / 데이터 삭제:

```bash
docker compose -f deploy/oss/docker-compose.yml down      # DB 볼륨 유지
docker compose -f deploy/oss/docker-compose.yml down -v   # SQLite·시크릿까지 삭제
```

**이미지 빌드**가 이렇게 실패하면:

```text
x509: certificate signed by unknown authority
```

회사망이 TLS를 가로채는 경우가 많습니다. **B. Go**로 가거나, 조직 Root CA를 빌드에 넣으세요.

### B. Go (Docker 없이)

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms
mkdir -p data/secrets bin

go build -o bin/llms-gateway ./cmd/llms-gateway
go build -o bin/llms ./cmd/llms
```

Windows Git Bash에서는 `bin/llms-gateway.exe` / `bin/llms.exe`가 될 수 있습니다.

**터미널 1 — 끄지 마세요:**

```bash
export HTTP_ADDR=127.0.0.1:8080
export DATABASE_URL=sqlite:./data/llms.db
export LLMS_SECRETS_DIR=./data/secrets
export BOOTSTRAP_TOKEN=local-dev-bootstrap

./bin/llms-gateway
```

| 환경변수 | 의미 |
|----------|------|
| `HTTP_ADDR` | 리슨 주소 (`127.0.0.1:8080` = 이 PC만) |
| `DATABASE_URL` | SQLite 파일, 예: `sqlite:./data/llms.db` |
| `LLMS_SECRETS_DIR` | 시크릿 파일 디렉터리 |
| `BOOTSTRAP_TOKEN` | 최초 설정의 `X-Bootstrap-Token`과 같아야 함 |

`./data/`는 커밋하지 마세요.

Postgres로 돌리고 싶다면 `make dev-db`로 Docker에 `127.0.0.1:54329` Postgres를 띄웁니다(`deploy/dev/compose.yaml`).
`./bin/llms-gateway` 전에 `export DATABASE_URL=postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable`을 설정하세요.
끌 때는 `make dev-db-down`이고, 데이터는 Docker 볼륨에 남습니다. 로컬 개발용입니다.

---

## 2. 스모크 체크 (다른 작업 전에)

**터미널 2** (게이트웨이는 켠 채):

```bash
curl -sS http://127.0.0.1:8080/health
curl -sS http://127.0.0.1:8080/ready
```

기대 응답 (필드가 더 있어도 `status`가 핵심):

```json
{"status":"ok"}
```

```json
{"status":"ready"}
```

`curl: Failed to connect` → 게이트웨이가 안 떠 있음 (§1 다시, 그 터미널 유지).

게이트웨이는 마이그레이션과 DB 연결이 끝나기 전에 먼저 포트를 엽니다. 그동안 `/health`와 `/livez`는 바로 200을, `/readyz`는 503 `{"status":"starting"}`을 돌려주고(시작이 끝나면 200 `{"status":"ready"}`), 나머지 경로는 503 `{"error":"starting"}`을 돌려줍니다. 오케스트레이터 probe에는 `/livez`(liveness)와 `/readyz`(readiness)를 쓰세요. `/readyz`는 DB를 직접 확인하지 않으므로 DB 전환 중에도 모든 레플리카가 한꺼번에 빠지지 않습니다. 모니터링용 `/ready`는 지금처럼 DB를 확인합니다.

---

## 3. 첫 end-to-end 테스트 (curl)

아래는 **OpenAI 호환 API 키** 벤더로 OAuth 없이 검증하는 경로입니다.  
살아 있는 모델 응답이 필요하면 `sk-…`를 실제 업스트림 키로 바꾸세요.

### 3.1 부트스트랩 → 게이트웨이 API 키

```bash
curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}'
```

응답 예:

```json
{
  "api_key": "sk-gt-…",
  "warning": "store api_key now; it will not be shown again"
}
```

한 번만 보이니 저장:

```bash
export LLMS_API_BASE=http://127.0.0.1:8080
export LLMS_API_KEY='sk-gt-…'   # JSON 값 붙여넣기
```

`jq`가 있으면:

```bash
BOOT=$(curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}')
echo "$BOOT" | jq .
export LLMS_API_KEY="$(echo "$BOOT" | jq -r .api_key)"
export LLMS_API_BASE=http://127.0.0.1:8080
```

> 이미 초기화된 DB에 부트스트랩을 다시 치면 실패할 수 있습니다. 깨끗이 하려면 볼륨 삭제 / `./data` 삭제 후 재기동.

### 3.2 업스트림 계정 추가

```bash
curl -sS -X POST "$LLMS_API_BASE/control/v1/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"vendor":"openai","name":"main","api_key":"sk-…"}'
```

응답의 계정 `id`(UUID)를 저장:

```bash
export ACCOUNT_ID='…'   # JSON "id"
```

### 3.3 라우트 생성 + 계정 연결

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

이 라우트의 공개 OpenAI 호환 주소:

```text
http://127.0.0.1:8080/r/default/v1
```

### 3.4 chat completions 테스트

```bash
curl -sS "$LLMS_API_BASE/r/default/v1/chat/completions" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

성공 시 일반 OpenAI chat 형태(`choices[0].message.content` 등).  
업스트림 인증 오류는 벤더 키/계정 문제 — 게이트웨이 health는 OK일 수 있습니다.

라우팅 바꾸기:

```bash
llms route update default --preset quota-first
```

---

## 4. 같은 흐름을 CLI로

게이트웨이 실행 중 + `LLMS_API_BASE` / `LLMS_API_KEY` 설정 후:

```bash
# Docker만 썼다면 CLI는 한 번 빌드
go build -o bin/llms ./cmd/llms

./bin/llms status
./bin/llms status --refresh

./bin/llms add
# 안내: OAuth(ChatGPT / Claude / Codex) 또는 API 키 붙여넣기

./bin/llms route create
# 위저드: slug, 계정, 프리셋 (기본은 quota-first 쪽)

./bin/llms route list
./bin/llms route url default
./bin/llms models default
./bin/llms env default
```

일상:

```bash
./bin/llms route update default --preset reset-soon
./bin/llms route key default      # 라우트 스코프 sk-gt (한 번만 표시)
./bin/llms disconnect openai:main
./bin/llms key list
./bin/llms credentials
```

| 프리셋 (`--preset`) | 동작 |
|---------------------|------|
| `failover` | 될 때까지 순서대로 |
| `fill-first` | 건강한 첫 좌석을 다 쓴 뒤 다음 |
| `balance` | 건강한 좌석 라운드로빈 |
| `prefer-primary` | 프라이머리 소프트 선호 (~80/20) |
| `quota-first` | 남은 한도 많은 쪽 (동률이면 리셋 빠른 쪽) |
| `reset-soon` | 창이 곧 리셋되는 좌석 우선 |
| `steward` | 남은량 × 리셋 긴급도 (OAuth 쪽 기울기) |
| `parallel` | 빌드/플랜에서 켜진 경우 팬아웃 |

호스팅용 (`llms login`, `llms plan`, `llms free`, `llms upgrade`)은 [llms.goodtek.xyz](https://llms.goodtek.xyz)용입니다. 로컬 OSS는 부트스트랩 후 무시해도 됩니다.

---

## 5. Cursor / 에이전트에 붙이기

| 설정 | 값 |
|------|-----|
| Base URL | `http://127.0.0.1:8080/r/default/v1` |
| API key | `sk-gt-…` (또는 `llms route key default`) |
| Model | `llms models default`에서 고른 id |

OpenAI Chat Completions와 같은 형태라 Base URL + 키만 있으면 됩니다.

---

## 6. 자동 스모크 (선택)

```bash
./deploy/oss/smoke.sh
go test ./internal/httpserver/ -run TestOSSE2E -count=1 -v
```

`smoke.sh`는 다른 포트에 임시 게이트웨이를 띄워 `/health`·`/ready`를 확인합니다 (Docker 불필요).

---

## 7. 데이터 · 백업

| 경로 | 용도 |
|------|------|
| Docker: 볼륨 `/data/llms.db` · Go: `./data/llms.db` | SQLite |
| Docker: `/data/secrets/` · Go: `./data/secrets/` | 시크릿 파일 |

복구가 필요하면 둘 다 백업. 커밋하지 마세요.

---

## 8. 안 될 때

| 증상 | 조치 |
|------|------|
| `:8080`에 `Failed to connect` | 게이트웨이 미기동 — §1 다시, 터미널 유지 |
| `health`/`ready` 비정상 | 포트 충돌? 로그: `docker compose … logs -f gateway` 또는 stdout |
| `401` | `LLMS_API_KEY` 틀림/없음; 데이터 지우고 재부트스트랩 |
| `bootstrap` 거절 | 헤더 `X-Bootstrap-Token: local-dev-bootstrap` |
| Docker 빌드 `x509 … unknown authority` | 회사 TLS → **Go** 경로 |
| `sqlite … unable to open` / `out of memory (14)` | 볼륨 권한. Docker면 `down -v` 후 `up --build`, Go면 `./data` 소유권 확인 |
| 모델 비었음 / 업스트림 오류 | `llms add` 다시; 벤더 키 확인; `llms status --refresh` |
| 에디터 연결 안 됨 | Base URL에 `/r/<slug>/v1` 포함 |
| 로컬호스트만 | 샘플이 `127.0.0.1`만 바인딩 (의도) |

compose 메모: [`deploy/oss/README.md`](deploy/oss/README.md).

---

## 저장소 지도

| 경로 | 역할 |
|------|------|
| `cmd/llms-gateway` | HTTP 게이트웨이 |
| `cmd/llms` | CLI |
| `internal/` | 라우팅, 쿼터, 벤더, 스토어 |
| `deploy/oss/` | 로컬 Docker 샘플 + 스모크 스크립트 |

---

## 아닌 것

- 토큰 마켓플레이스 아님 (**본인** 좌석 사용)
- “가짜 공식 CLI” / 클로킹 프록시 아님
- “무제한 ChatGPT API” 마케팅 아님
- 호스팅 관리/결제 UI 아님 (클라우드 전용)

---

## 라이선스

[MIT](LICENSE).

---

## goodtek에서

[goodtek](https://goodtek.xyz)은 **신뢰·안전·운영 가능한 시스템**을 우선하는 AI 제품 스튜디오입니다. 경계를 분명히 두고, 과장 없이, 프로덕션에서 바로 굴릴 수 있는 도구를 만듭니다.

| | |
|---|---|
| **goodtek** | 믿을 수 있는 제품 스튜디오 — [goodtek.xyz](https://goodtek.xyz) |
| **openllms / llms** | 구독 → API 게이트웨이 (이 프로젝트) — 호스팅 [llms.goodtek.xyz](https://llms.goodtek.xyz) |
| **vibePulse** | 사이트·API·에이전트 상시 모니터링 — [vibepulse.goodtek.xyz](https://vibepulse.goodtek.xyz) |
| **VibeCrew** | 바이브코딩 빌더 커뮤니티 — [vibecrew.kr](https://vibecrew.kr) |

문의: [hello@goodtek.xyz](mailto:hello@goodtek.xyz) · X [@goodtek_xyz](https://x.com/goodtek_xyz) · Threads [@goodtek.xyz](https://www.threads.net/@goodtek.xyz)
