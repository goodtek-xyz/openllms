# Install & register — llms.goodtek.xyz

> 다른 머신·다른 서비스에서 **goodtek 클라우드** CLI를 설치하고 등록하는 절차입니다.  
> 셀프호스트는 공개 엔진 [openllms](https://github.com/goodtek-xyz/openllms)를 보세요.

**Gateway:** `https://llms.goodtek.xyz`  
**사람용 제품 소개:** https://llms.goodtek.xyz/ (KO) · [/en](https://llms.goodtek.xyz/en) · [/ja](https://llms.goodtek.xyz/ja) · [/zh](https://llms.goodtek.xyz/zh)  
**사람용 설치 가이드:** https://llms.goodtek.xyz/install (KO) · [/en/install](https://llms.goodtek.xyz/en/install) · [/ja/install](https://llms.goodtek.xyz/ja/install) · [/zh/install](https://llms.goodtek.xyz/zh/install)  
**기계용 문서 (공개, 인증 없음):** https://llms.goodtek.xyz/install.md  
**설치 스크립트 (공개):** https://llms.goodtek.xyz/install.sh  
**CLI dist (공개):** https://llms.goodtek.xyz/dist/ · `llms_{os}_{arch}`  
**메타:** `GET https://llms.goodtek.xyz/control/v1/meta` → `landing`, `install_html`, `landing_i18n`, `install_html_i18n`, `install_doc`, `install_sh`, `cli_dist`, `cli_version`, `unified_api_doc`, `api_surface`  
**통합 API (에이전트/클라이언트):** https://llms.goodtek.xyz/LLMS_API.md — `POST /r/{slug}/v1/chat/completions` only

다른 서비스에서 문서를 읽어올 때:

```bash
curl -fsSL https://llms.goodtek.xyz/install.md
```

---

## 사전 조건

| 항목 | 설명 |
|------|------|
| OS | Linux 또는 macOS (`amd64` / `arm64`) |
| 네트워크 | `llms.goodtek.xyz` (스크립트 + `/dist` 바이너리), 벤더 OAuth(연결 시) |
| GitHub | `llms login`용 계정 + org가 허용한 OAuth App Device Flow |
| 바이너리 | **권장:** 게이트웨이 `/dist` (인증 없음, `SHA256SUMS` 검증). openllms GitHub Releases도 같은 방식으로 검증. |

---

## 1. CLI 설치

### A. 권장 — 공개 게이트웨이에서 스크립트 실행

```bash
curl -fsSL https://llms.goodtek.xyz/install.sh | bash
llms version
```

기본 설치 위치: `/usr/local/bin/llms` (쓰기 권한 없으면 `sudo` 사용).

다른 디렉터리:

```bash
LLMS_BIN_DIR="$HOME/bin" curl -fsSL https://llms.goodtek.xyz/install.sh | bash
export PATH="$HOME/bin:$PATH"
```

### B. 직접 `/dist`에서 받기

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fsSL "https://llms.goodtek.xyz/dist/llms_${OS}_${ARCH}" -o "llms_${OS}_${ARCH}"
curl -fsSL "https://llms.goodtek.xyz/dist/SHA256SUMS" | grep " llms_${OS}_${ARCH}$" | shasum -a 256 -c -
mv "llms_${OS}_${ARCH}" llms
chmod +x llms
sudo mv llms /usr/local/bin/llms
```

### C. GitHub Releases (openllms)

공개 저장소 [goodtek-xyz/openllms](https://github.com/goodtek-xyz/openllms/releases)의 Release에서 받습니다. 바이너리마다
`SHA256SUMS`와 빌드 출처 증명(artifact attestation)이 함께 올라갑니다.

```bash
LLMS_INSTALL_SOURCE=github curl -fsSL https://llms.goodtek.xyz/install.sh | bash
# 특정 버전
LLMS_INSTALL_SOURCE=github LLMS_VERSION=v0.2.0 curl -fsSL https://llms.goodtek.xyz/install.sh | bash
# 직접 받은 파일 검증
shasum -a 256 -c SHA256SUMS --ignore-missing
gh attestation verify llms_<os>_<arch> --repo goodtek-xyz/openllms
```

이미 설치돼 있으면 `llms update`가 연결된 게이트웨이의 `/dist`에서 그 자리에 업데이트합니다.

설치 확인:

```bash
llms version
llms --help
```

---

## 2. 로그인 (goodtek 세션)

```bash
llms login
```

1. 터미널에 나오는 URL을 연다.  
2. GitHub device code를 입력·승인한다.  
3. 게이트웨이가 `sk-gt-…`를 발급하고 `~/.config/llms/credentials.json`에 저장한다.

기본 API Base는 `https://llms.goodtek.xyz` 입니다.  
다른 게이트웨이를 쓸 때만:

```bash
llms login --api-base https://llms.goodtek.xyz
# 또는
export LLMS_API_BASE=https://llms.goodtek.xyz
```

`GITHUB_CLIENT_ID`는 보통 게이트웨이 `GET /control/v1/meta`에서 CLI가 가져옵니다. 안 되면:

```bash
export GITHUB_CLIENT_ID=…   # ops가 알려준 Client ID
llms login
```

확인:

```bash
llms status
```

로그아웃(로컬 자격만 삭제, 서버 키는 유지):

```bash
llms logout
```

---

## 3. 업스트림 계정 등록

```bash
llms add
```

| 벤더 | 방식 |
|------|------|
| **Claude** | Browser login (URL 연 뒤 authorization code paste) 또는 API key |
| **Codex** | ChatGPT device code (`auth.openai.com/codex/device`) 또는 API key |
| **DeepSeek / OpenAI / Kimi / GLM** | API key (+ 선택 Base URL) |

이름은 `work`, `personal`처럼 짧게. 상태판에는 `vendor:name`으로 보입니다.

여러 계정:

```bash
llms add   # 반복
llms status --refresh   # Codex/Claude oauth 쿼터 갱신
```

계정 제거:

```bash
llms disconnect codex:work
# 또는 account UUID
llms disconnect <account-id>
```

---

## 4. 라우트 만들기 (클라이언트용 URL)

```bash
llms route create
llms route list
llms route update codex-quota-first          # interactive: preset + accounts
# or non-interactive:
# llms route update codex-quota-first --preset failover --accounts <id1>,<id2>
llms route rm old-slug --yes
```

1. slug (예: `claude-failover`)  
2. 프리셋: failover / balance / prefer-primary / quota-first / parallel  
3. 계정 멀티 선택  

URL만 보기:

```bash
llms route url claude-failover
# → https://llms.goodtek.xyz/r/claude-failover/v1
```

**클라이언트 전용 키** (한 번만 표시 — 저장 필수):

```bash
llms route key claude-failover
```

셸용:

```bash
llms env claude-failover
# export OPENAI_BASE_URL=...
# export OPENAI_API_KEY=...
```

모델 목록:

```bash
llms models claude-failover
llms models --filter gpt
```

프로젝트 키 관리:

```bash
llms key list
llms key create laptop
llms key revoke <key-id>
```

다른 기기에서도 `llms login`만 하면 됩니다. 그 기기에 키가 없으면 그 기기 전용 키(`cli:<호스트 이름>`)가 새로 발급되고,
다른 기기와 앱이 쓰는 키는 그대로 유지됩니다. 기기를 더 쓰지 않으면 `llms key list`에서 그 키를 찾아 `llms key revoke`로 폐기합니다.
`llms login --rotate-key`는 `cli` 키를 모두 폐기하고 새로 만듭니다(그 키로 로그인한 다른 기기는 다시 로그인해야 합니다).

---

## 5. 클라이언트에 붙이기

**어디서 연결할지 몰라도 됩니다.** IDE·CLI·봇·스크립트·자동화 등 OpenAI 호환(또는 Anthropic Messages) Base URL을 받는 도구면 동일합니다. 특정 제품 전용이 아닙니다.

필요한 것은 둘뿐입니다.

| 설정 | 값 |
|------|-----|
| **Base URL** | `https://llms.goodtek.xyz/r/<slug>/v1` |
| **API Key** | `llms route key <slug>` 로 받은 `sk-gt-…` |

도구 설정 화면에서 이름이 `OpenAI Base URL` / `API Base` / `Custom endpoint` 등으로 달라도, 위 URL과 `sk-gt`만 넣으면 됩니다.

Anthropic Messages 형태 클라이언트는 같은 라우트의 messages 경로를 씁니다 (`/r/<slug>/v1/messages` 또는 `/r/<slug>/messages`).

스모크 (클라이언트가 없어도 검증 가능):

```bash
curl -sS https://llms.goodtek.xyz/r/<slug>/v1/chat/completions \
  -H "Authorization: Bearer sk-gt-…" \
  -H "Content-Type: application/json" \
  -d '{"model":"<model-id>","messages":[{"role":"user","content":"ping"}],"max_tokens":16}'
```

`model`은 `llms models <slug>`에 나온 **실제 모델 id**를 쓰세요 (`codex:work` 같은 계정 ref가 아닙니다).

이미지 생성 (OpenAI Images API — API 키 계정 또는 Codex OAuth; Claude 제외. Codex는 `b64_json` 반환):

```bash
curl -sS https://llms.goodtek.xyz/r/<slug>/v1/images/generations \
  -H "Authorization: Bearer sk-gt-…" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-1","prompt":"a small red cube","size":"1024x1024"}'
```

---

## 6. 일상 점검

```bash
llms status              # 계정 헬스 + 쿼터 바 + 라우트
llms status --refresh    # Codex/Claude oauth 쿼터 재조회 후 보드
```

| Glyph | 의미 |
|-------|------|
| `● ok` | 사용 가능 |
| `○ cooldown` | 429 등으로 잠시 스킵 |
| `✖ error` | 재연결/`llms add` 필요 |

---

## 환경 변수 요약

| 변수 | 용도 |
|------|------|
| `LLMS_API_BASE` | 게이트웨이 Base (기본 `https://llms.goodtek.xyz`) |
| `LLMS_API_KEY` | 자격 파일 대신 Bearer (CI 등) |
| `LLMS_DIST_BASE` | `install.sh`가 받을 게이트웨이 (기본 prod URL) |
| `LLMS_INSTALL_SOURCE` | `dist` (기본) 또는 `github` |
| `GITHUB_CLIENT_ID` | meta에 없을 때 device login용 |
| `LLMS_BIN_DIR` | `install.sh` 설치 경로 (기본: PATH에 이미 있는 `llms`의 위치, 없으면 `/usr/local/bin`) |
| `LLMS_VERSION` | GitHub 폴백 시 릴리스 태그 (기본 `latest`) |
| `LLMS_INSTALL_REPO` | GitHub 폴백 저장소 (기본 `goodtek-xyz/openllms`) |

자격 파일: `~/.config/llms/credentials.json` (mode `0600`).

---

## 문제 해결

| 증상 | 조치 |
|------|------|
| `install.sh` 404 / download failed | `https://llms.goodtek.xyz/install.sh` + `/dist/llms_*` 확인. meta의 `cli_dist_ready`가 false면 배포 후 dist 빌드 점검 |
| raw.githubusercontent.com 404 | 레포 private — 공개 URL(`llms.goodtek.xyz`)을 쓰세요 |
| `not logged in` | `llms login` |
| `GITHUB_CLIENT_ID required` | 게이트웨이 기동·`/control/v1/meta` 확인, 또는 env로 Client ID |
| 라우트 404 | slug 확인, `llms status`의 ROUTES |
| 업스트림 401 | oauth면 `llms add` 재연결; API key면 키/Base URL 확인 |
| 클라이언트가 localhost로 붙음 | `llms route url` / `llms env`로 클라우드 URL인지 확인. `LLMS_API_BASE`·로그인 시점 Base 점검 |

운영/배포(서버 쪽): [RUNBOOK.md](RUNBOOK.md) · [HUMAN-SETUP.md](HUMAN-SETUP.md)

공개 설치 문서(게이트웨이, 인증 없음): `https://llms.goodtek.xyz/install.md`

---

## 한 줄 치트시트

```bash
curl -fsSL https://llms.goodtek.xyz/install.md      # 이 문서
curl -fsSL https://llms.goodtek.xyz/install.sh | bash
llms login
llms add
llms route create
llms route list
llms route update <slug>
llms route key <slug>    # Base URL + Key → 클라이언트에 붙여넣기
llms status --refresh
```
