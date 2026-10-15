# openllms

[![GitHub stars](https://img.shields.io/github/stars/goodtek-xyz/openllms?style=social)](https://github.com/goodtek-xyz/openllms)

すでに払っている ChatGPT / Claude（および Codex）の席を、**OpenAI 互換 API ひとつ**にまとめます。  
残りの枠を見てトラフィックを振り分け、片方だけ燃え尽きて他が遊んでいる状態を減らします。

セルフホストは MIT で**ずっと無料**です。ゲートウェイ自体に利用メーターはありません。役に立ったら [★ Star](https://github.com/goodtek-xyz/openllms)。

[English](README.md) · [한국어](README.ko.md) · [中文](README.zh.md) · [MIT](LICENSE) · [ホスト版](https://llms.goodtek.xyz)

---

## できること

- Cursor / Claude Code / Codex / スクリプト向けの **Base URL ひとつ**
- 1 ルートに **複数アカウント** — 枠切れ・クーリング・エラーで次へ
- **クォータ意識のルーティング** — `llms status` で残量とリセット時刻
- **ローカル優先の OSS** — Docker *または* Go バイナリ + SQLite + ディスク上のファイル

このリポジトリは [llms](https://llms.goodtek.xyz) のオープンなエンジンです（[goodtek](https://goodtek.xyz) が作っています）。ホスト版の課金 UI はここにはありません。

---

## 起動パスを選ぶ

| | **A. Docker** | **B. Go** |
|--|---------------|-----------|
| 必要 | Docker Desktop / Engine + Compose | Go ツールチェーン（`go.mod` → 現在 **1.24**） |
| 向いているとき | コマンド一本で済ませたい | 社内 TLS で Docker 内 `go mod` が落ちる |
| データ | Docker ボリューム `oss-data` | `./data/llms.db` + `./data/secrets/` |
| 待ち受け | `127.0.0.1:8080` | `127.0.0.1:8080`（自分で設定） |

以下で使うローカル用ブートストラップトークン: **`local-dev-bootstrap`**  
（ローカル専用 — 公開インタフェースに出さないでください）

---

## 1. インストール & 起動

### A. Docker

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms

# フォアグラウンド — このターミナルにログ（Ctrl+C で停止）
docker compose -f deploy/oss/docker-compose.yml up --build
```

バックグラウンド:

```bash
docker compose -f deploy/oss/docker-compose.yml up --build -d
docker compose -f deploy/oss/docker-compose.yml ps
docker compose -f deploy/oss/docker-compose.yml logs -f gateway
```

正常起動のログ例:

```text
llms-gateway listening ... addr=0.0.0.0:8080
```

（`listening` / `database backend` 系なら OK）

停止 / データ削除:

```bash
docker compose -f deploy/oss/docker-compose.yml down      # DB ボリュームは残す
docker compose -f deploy/oss/docker-compose.yml down -v   # SQLite・シークレットも削除
```

**イメージビルド**が次で失敗する場合:

```text
x509: certificate signed by unknown authority
```

社内網が TLS を横取りしていることが多いです。**B. Go** にするか、組織 Root CA をビルドに入れてください。

### B. Go（Docker なし）

```bash
git clone https://github.com/goodtek-xyz/openllms.git
cd openllms
mkdir -p data/secrets bin

go build -o bin/llms-gateway ./cmd/llms-gateway
go build -o bin/llms ./cmd/llms
```

Windows Git Bash では `bin/llms-gateway.exe` / `bin/llms.exe` になることがあります。

**ターミナル 1 — 閉じない:**

```bash
export HTTP_ADDR=127.0.0.1:8080
export DATABASE_URL=sqlite:./data/llms.db
export LLMS_SECRETS_DIR=./data/secrets
export BOOTSTRAP_TOKEN=local-dev-bootstrap

./bin/llms-gateway
```

| 環境変数 | 意味 |
|----------|------|
| `HTTP_ADDR` | 待ち受け（`127.0.0.1:8080` = このマシンのみ） |
| `DATABASE_URL` | SQLite ファイル、例: `sqlite:./data/llms.db` |
| `LLMS_SECRETS_DIR` | シークレットディレクトリ |
| `BOOTSTRAP_TOKEN` | 初回の `X-Bootstrap-Token` と一致させる |

`./data/` はコミットしないでください。

---

## 2. スモーク確認（他の作業の前に）

**ターミナル 2**（ゲートウェイは起動したまま）:

```bash
curl -sS http://127.0.0.1:8080/health
curl -sS http://127.0.0.1:8080/ready
```

期待（フィールドが増えても `status` が本丸）:

```json
{"status":"ok"}
```

```json
{"status":"ready"}
```

`curl: Failed to connect` → ゲートウェイ未起動（§1 をやり直し、そのターミナルを維持）。

ゲートウェイはマイグレーションと DB 接続が終わる前にポートを開きます。その間 `/health` と `/livez` はすぐ 200、`/readyz` は 503 `{"status":"starting"}`（起動完了後は 200 `{"status":"ready"}`）、他のパスは 503 `{"error":"starting"}` を返します。オーケストレーターの probe には `/livez`（liveness）と `/readyz`（readiness）を使ってください。`/readyz` は DB を直接確認しないため、DB の切り替え中でも全レプリカが同時に外れることはありません。監視用の `/ready` は従来どおり DB を確認します。

---

## 3. 最初の end-to-end テスト（curl）

**OpenAI 互換 API キー**ベンダーで、OAuth なしに通す経路です。  
実モデル応答が欲しければ `sk-…` を本物の upstream キーに差し替えてください。

### 3.1 ブートストラップ → ゲートウェイ API キー

```bash
curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}'
```

レスポンス例:

```json
{
  "api_key": "sk-gt-…",
  "warning": "store api_key now; it will not be shown again"
}
```

一度しか出ないので保存:

```bash
export LLMS_API_BASE=http://127.0.0.1:8080
export LLMS_API_KEY='sk-gt-…'   # JSON の値を貼る
```

`jq` がある場合:

```bash
BOOT=$(curl -sS -X POST http://127.0.0.1:8080/control/v1/bootstrap \
  -H "X-Bootstrap-Token: local-dev-bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"login":"me","project_name":"default","key_name":"cli"}')
echo "$BOOT" | jq .
export LLMS_API_KEY="$(echo "$BOOT" | jq -r .api_key)"
export LLMS_API_BASE=http://127.0.0.1:8080
```

> 初期化済み DB への再ブートストラップは失敗することがあります。やり直すならボリューム削除 / `./data` 削除後に再起動。

### 3.2 upstream アカウント追加

```bash
curl -sS -X POST "$LLMS_API_BASE/control/v1/accounts" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"vendor":"openai","name":"main","api_key":"sk-…"}'
```

レスポンスのアカウント `id`（UUID）を保存:

```bash
export ACCOUNT_ID='…'   # JSON "id"
```

### 3.3 ルート作成 + アカウント接続

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

このルートの公開 OpenAI 互換アドレス:

```text
http://127.0.0.1:8080/r/default/v1
```

### 3.4 chat completions テスト

```bash
curl -sS "$LLMS_API_BASE/r/default/v1/chat/completions" \
  -H "Authorization: Bearer $LLMS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

成功時は通常の OpenAI chat 形（`choices[0].message.content` など）。  
upstream 認証エラーはベンダーキー／アカウントの問題 — ゲートウェイ health は OK のこともあります。

ルーティング変更:

```bash
llms route update default --preset quota-first
```

---

## 4. 同じ流れを CLI で

ゲートウェイ起動中 + `LLMS_API_BASE` / `LLMS_API_KEY` 設定後:

```bash
# Docker だけの場合は CLI を一度ビルド
go build -o bin/llms ./cmd/llms

./bin/llms status
./bin/llms status --refresh

./bin/llms add
# 案内: OAuth（ChatGPT / Claude / Codex）または API キー貼り付け

./bin/llms route create
# ウィザード: slug、アカウント、プリセット（既定は quota-first 寄り）

./bin/llms route list
./bin/llms route url default
./bin/llms models default
./bin/llms env default
```

日常:

```bash
./bin/llms route update default --preset reset-soon
./bin/llms route key default      # ルートスコープ sk-gt（一度だけ表示）
./bin/llms disconnect openai:main
./bin/llms key list
./bin/llms credentials
```

| プリセット（`--preset`） | 動作 |
|--------------------------|------|
| `failover` | 通るまで順に試す |
| `fill-first` | 健全な先頭席を使い切ってから次へ |
| `balance` | 健全席のラウンドロビン |
| `prefer-primary` | プライマリをソフト優先（~80/20） |
| `quota-first` | 残り枠が多い方（同率ならリセットが早い方） |
| `reset-soon` | 窓のリセットが近い席を優先 |
| `steward` | 残量 × リセット緊急度（OAuth 寄り） |
| `parallel` | ビルド/プランで有効なときのファンアウト |

ホスト版向け（`llms login` / `llms plan` / `llms free` / `llms upgrade`）は [llms.goodtek.xyz](https://llms.goodtek.xyz) 用です。ローカル OSS ではブートストラップ後は無視して構いません。

---

## 5. Cursor / エージェントに向ける

| 設定 | 値 |
|------|-----|
| Base URL | `http://127.0.0.1:8080/r/default/v1` |
| API key | `sk-gt-…`（または `llms route key default`） |
| Model | `llms models default` の id |

OpenAI Chat Completions と同じ形なので、多くのツールは Base URL + キーだけで足ります。

---

## 6. 自動スモーク（任意）

```bash
./deploy/oss/smoke.sh
go test ./internal/httpserver/ -run TestOSSE2E -count=1 -v
```

`smoke.sh` は別ポートに一時ゲートウェイを立てて `/health`・`/ready` を見ます（Docker 不要）。

---

## 7. データとバックアップ

| パス | 用途 |
|------|------|
| Docker: ボリューム `/data/llms.db` · Go: `./data/llms.db` | SQLite |
| Docker: `/data/secrets/` · Go: `./data/secrets/` | シークレットファイル |

復元するなら両方バックアップ。コミットしないでください。

---

## 8. うまくいかないとき

| 症状 | 対処 |
|------|------|
| `:8080` に `Failed to connect` | ゲートウェイ未起動 — §1 をやり直し、ターミナルを維持 |
| `health`/`ready` 異常 | ポート衝突？ ログ: `docker compose … logs -f gateway` または stdout |
| `401` | `LLMS_API_KEY` 誤り/未設定。データを消して再ブートストラップ |
| `bootstrap` 拒否 | ヘッダは `X-Bootstrap-Token: local-dev-bootstrap` |
| Docker ビルド `x509 … unknown authority` | 社内 TLS → **Go** 経路 |
| `sqlite … unable to open` / `out of memory (14)` | ボリューム権限。Docker は `down -v` 後 `up --build`、Go は `./data` の所有者を確認 |
| モデル空 / upstream エラー | `llms add` やり直し、ベンダーキー確認、`llms status --refresh` |
| エディタが繋がらない | Base URL に `/r/<slug>/v1` を含める |
| localhost のみ | サンプルは意図的に `127.0.0.1` のみ |

compose メモ: [`deploy/oss/README.md`](deploy/oss/README.md)。

---

## リポジトリ地図

| パス | 役割 |
|------|------|
| `cmd/llms-gateway` | HTTP ゲートウェイ |
| `cmd/llms` | CLI |
| `internal/` | ルーティング、クォータ、ベンダー、ストア |
| `deploy/oss/` | ローカル Docker サンプル + スモーク |

---

## これは何かではない

- トークンマーケットプレイスではない（**自分の**席を使う）
- 「偽の公式 CLI」/ クローキングプロキシではない
- 「無制限 ChatGPT API」の売り文句ではない
- ホスト版の管理/課金 UI ではない（クラウド専用）

---

## ライセンス

[MIT](LICENSE).

---

## goodtek より

[goodtek](https://goodtek.xyz) は、**信頼・安全・運用できるシステム**を大切にする AI プロダクトスタジオです。境界をはっきりさせ、過大表現を避け、本番で回せる道具をつくります。

| | |
|---|---|
| **goodtek** | 信頼できるプロダクトスタジオ — [goodtek.xyz](https://goodtek.xyz) |
| **openllms / llms** | サブスク → API ゲートウェイ（本プロジェクト） — ホスト版 [llms.goodtek.xyz](https://llms.goodtek.xyz) |
| **vibePulse** | サイト・API・エージェントの常時監視 — [vibepulse.goodtek.xyz](https://vibepulse.goodtek.xyz) |
| **VibeCrew** | バイブコーディングのビルダーコミュニティ — [vibecrew.kr](https://vibecrew.kr) |

連絡先: [hello@goodtek.xyz](mailto:hello@goodtek.xyz) · X [@goodtek_xyz](https://x.com/goodtek_xyz) · Threads [@goodtek.xyz](https://www.threads.net/@goodtek.xyz)
