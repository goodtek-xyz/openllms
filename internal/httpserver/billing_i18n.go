package httpserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

type billingCopy struct {
	Lang           string
	Title          string
	Skip           string
	Kicker         string
	H1             string
	Lead           string
	Sub            string
	Trust          string
	CTAStart       string
	CTASignal      string
	WhyTitle       string
	Why1Title      string
	Why1Body       string
	Why2Title      string
	Why2Body       string
	Why3Title      string
	Why3Body       string
	PlanStatus     string
	Loading        string
	FreeStart      string
	CLIGuide       string
	PlansTitle     string
	PlansLead      string
	FreeTag        string
	PayNote        string
	RouteTitle     string
	RouteLead      string
	RouteDiff      string
	HonestyTitle   string
	HonestyBody    string
	RouteFTitle    string
	RouteFBody     string
	RouteFillTitle string
	RouteFillBody  string
	RouteBTitle    string
	RouteBBody     string
	RoutePTitle    string
	RoutePBody     string
	RouteQTitle    string
	RouteQBody     string
	RouteRTitle    string
	RouteRBody     string
	RouteSTitle    string
	RouteSBody     string
	HowTitle       string
	HowLead        string
	Note           string
	NeedAuth       string
	Unlimited      string
	None           string
	Yes            string
	No             string
	Plan           string
	Status         string
	Entitled       string
	Limits         string
	Usage          string
	Accounts       string
	Routes         string
	Keys           string
	PeriodSuffix   string
	FreeFail       string
	FreeOK         string
	FreeFeat1      string
	FreeFeat2      string
	FreeFeat3      string
	CrewKicker     string
	CrewTitle      string
	CrewLead       string
	CrewWeek       string
	CrewTotal      string
	CrewNoteLabel  string
	CrewNotePH     string
	CrewCTA        string
	CrewBrands     string
	CrewOK         string
	CrewFail       string
}

func billingCopyFor(lang string) billingCopy {
	switch lang {
	case "en":
		return billingCopy{
			Lang: "en", Title: "llms — Cloud (free for now)", Skip: "Skip to content",
			Kicker: "Cloud by goodtek", H1: "Keep the subs. Get one API.",
			Lead:     "Point Cursor, IDEs, and bots at one OpenAI-compatible URL. We route across the ChatGPT and Claude accounts you already pay for and watch remaining quota.",
			Sub:      "Self-host when you want full control · Cloud when ops feel hard · Cloud is free for now",
			Trust:    `llms is built by <a href="https://goodtek.xyz/" target="_blank" rel="noopener noreferrer">goodtek</a>.`,
			CTAStart: "Get started", CTASignal: "I need cloud",
			WhyTitle:   "Why the cloud hub",
			Why1Title:  "One address",
			Why1Body:   "Several logins, one URL for Cursor, IDEs, and bots — same chat/completions shape you already know.",
			Why2Title:  "Your seats, not a token mall",
			Why2Body:   "Route the subscriptions and keys you already own, and see what’s left — not a token marketplace.",
			Why3Title:  "Cloud when you need it",
			Why3Body:   "Self-host with openllms if you want. If running a box feels heavy, use the hosted cloud — free for now while we listen.",
			PlanStatus: "Cloud status", Loading: "Loading…",
			FreeStart: "Get started", CLIGuide: "CLI guide",
			PlansTitle: "Cloud · free for now", PlansLead: "Hosted llms is free while we open carefully. Prefer your own server? openllms stays unlimited when you self-host.",
			FreeTag:      "Hosted",
			PayNote:      "If the cloud path matters to you — more seats, steadier ops — tell us below. We shape what’s next from real demand.",
			RouteTitle:   "Routing policies",
			RouteLead:    "Pick a policy per route URL. Failover is always on before the first token reaches your client — not just a model picker.",
			RouteDiff:    "llms orchestrates your accounts: remaining quota, health, and in-request failover.",
			HonestyTitle: "Honesty",
			HonestyBody:  "Official OAuth for accounts you own. No credential sharing. No CLI cloaking. Provider limits still apply.",
			RouteFTitle:  "failover", RouteFBody: "Try primary, then backup — ordered seats.",
			RouteFillTitle: "fill-first", RouteFillBody: "Burn one seat fully, then the next (stable order).",
			RouteBTitle: "balance", RouteBBody: "Round-robin across healthy accounts.",
			RoutePTitle: "prefer-primary", RoutePBody: "Soft 80/20 preference for your main seat.",
			RouteQTitle: "quota-first", RouteQBody: "Prefer more remaining; on ties, earliest reset.",
			RouteRTitle: "reset-soon", RouteRBody: "Burn the seat whose 7d window resets soonest (min remaining).",
			RouteSTitle: "steward", RouteSBody: "Score remaining × reset urgency (prefer OAuth seats; put paid API keys on fallback_slugs).",
			HowTitle: "How to activate", HowLead: "After install, sign in and turn on free cloud from the CLI:",
			Note:      "Accounts and routes: llms add · llms status · llms plan · llms signal.",
			NeedAuth:  "Sign in from the CLI first: llms login · then llms free, or tell us you need cloud here.",
			Unlimited: "Unlimited", None: "None", Yes: "Yes", No: "No",
			Plan: "Plan", Status: "Status", Entitled: "Entitled",
			Limits: "Limits", Usage: "Usage",
			Accounts: "accounts", Routes: "routes", Keys: "keys", PeriodSuffix: "/ mo",
			FreeFail: "Could not start free cloud", FreeOK: "Free cloud activated.",
			FreeFeat1: "1 account · 1 route · 1 key to start", FreeFeat2: "Hosted ops — no box to babysit", FreeFeat3: "Routing + remaining-quota board",
			CrewKicker: "Tell us", CrewTitle: "When you need the cloud",
			CrewLead:      "If self-host feels hard, or the hosted hub already matters for how you work — one click tells goodtek and VibeCrew. We use that to decide what to build next.",
			CrewWeek:      "builders said they need cloud this week",
			CrewTotal:     "All-time: {n}",
			CrewNoteLabel: "Optional note (what you’re building)",
			CrewNotePH:    "e.g. shipping a bot with Cursor…",
			CrewCTA:       "I need the cloud",
			CrewBrands:    "Built by",
			CrewOK:        "Got it — thanks. The counter just moved.",
			CrewFail:      "Could not send",
		}
	case "ja":
		return billingCopy{
			Lang: "ja", Title: "llms — クラウド（いま無料）", Skip: "本文へ",
			Kicker: "goodtek のクラウド", H1: "サブスクはそのまま。API はひとつ。",
			Lead:     "Cursor・IDE・ボットには OpenAI 互換 URL をひとつ。すでに払っている ChatGPT / Claude へ振り分け、残り枠も見ます。",
			Sub:      "自分で回すならセルフホスト · 運用が重いならクラウド · クラウドはいま無料",
			Trust:    `llms は <a href="https://goodtek.xyz/" target="_blank" rel="noopener noreferrer">goodtek</a> が作っています。`,
			CTAStart: "はじめる", CTASignal: "クラウドが必要",
			WhyTitle:   "クラウドハブの理由",
			Why1Title:  "アドレスひとつ",
			Why1Body:   "複数ログインをまとめて URL ひとつ。いつもの chat/completions です。",
			Why2Title:  "トークンモールではない",
			Why2Body:   "すでに持っている席を振り分け、残り枠も見ながら使います。",
			Why3Title:  "必要なときのクラウド",
			Why3Body:   "自分で回すなら openllms。箱の運用が重いと感じたらホスト版へ — いまは無料で開いています。",
			PlanStatus: "クラウド状態", Loading: "読み込み中…",
			FreeStart: "はじめる", CLIGuide: "CLI ガイド",
			PlansTitle: "クラウド · いま無料", PlansLead: "ホスト版 llms は慎重に開きながら無料です。自前サーバーなら openllms はセルフホストで無制限。",
			FreeTag:      "ホスト",
			PayNote:      "クラウドが自分の作業に必要だと感じたら、下から教えてください。次に作るものの判断材料になります。",
			RouteTitle:   "ルーティング方針",
			RouteLead:    "ルート URL ごとに方針を選びます。最初のトークンが届く前にフェイルオーバー — モデル一覧だけではありません。",
			RouteDiff:    "llms はあなたのアカウントをオーケストレーションします（残量・ヘルス・リクエスト内フェイルオーバー）。",
			HonestyTitle: "正直に",
			HonestyBody:  "公式 OAuth で自分のアカウントのみ。資格情報の共有なし。CLI 偽装なし。上限と ToS はそのまま。",
			RouteFTitle:  "failover", RouteFBody: "主系→予備の順で試す。",
			RouteFillTitle: "fill-first", RouteFillBody: "1席を使い切ってから次へ（固定順）。",
			RouteBTitle: "balance", RouteBBody: "健全なアカウントをラウンドロビン。",
			RoutePTitle: "prefer-primary", RoutePBody: "主席を 80/20 で優先。",
			RouteQTitle: "quota-first", RouteQBody: "残り枠が多い席を優先。同率ならリセットが近い方。",
			RouteRTitle: "reset-soon", RouteRBody: "7d 枠のリセットが近い席から使う。",
			RouteSTitle: "steward", RouteSBody: "残量×リセット緊急度。OAuth 席優先。有料 API キーは fallback_slugs へ。",
			HowTitle: "有効化", HowLead: "インストール後、CLI でログインして無料クラウドをオンに:",
			Note:      "アカウントとルート: llms add · llms status · llms plan · llms signal。",
			NeedAuth:  "先に CLI でログイン: llms login · その後 llms free、またはここでクラウド需要を送信。",
			Unlimited: "無制限", None: "なし", Yes: "はい", No: "いいえ",
			Plan: "プラン", Status: "状態", Entitled: "利用可",
			Limits: "上限", Usage: "使用量",
			Accounts: "アカウント", Routes: "ルート", Keys: "キー", PeriodSuffix: "/ 月",
			FreeFail: "無料クラウド開始に失敗", FreeOK: "無料クラウドが有効になりました。",
			FreeFeat1: "まず アカウント1 · ルート1 · キー1", FreeFeat2: "ホスト運用 — 箱の世話なし", FreeFeat3: "ルーティングと残量ボード",
			CrewKicker: "教えてください", CrewTitle: "クラウドが必要なとき",
			CrewLead:      "セルフホストが重い、またはホスト版がすでに仕事の一部なら — ワンクリックで goodtek と VibeCrew に伝わります。次に何を作るかの材料になります。",
			CrewWeek:      "人が今週クラウドが必要だと伝えました",
			CrewTotal:     "累計: {n}",
			CrewNoteLabel: "任意メモ（何を作っているか）",
			CrewNotePH:    "例: Cursor でボットを…",
			CrewCTA:       "クラウドが必要です",
			CrewBrands:    "制作",
			CrewOK:        "受け取りました — ありがとう。カウンターが動きました。",
			CrewFail:      "送信に失敗",
		}
	case "zh":
		return billingCopy{
			Lang: "zh", Title: "llms — 云端（目前免费）", Skip: "跳到正文",
			Kicker: "goodtek 云端", H1: "订阅照旧，API 合成一个。",
			Lead:     "给 Cursor、IDE、机器人一个 OpenAI 兼容地址。我们把你已在付费的 ChatGPT / Claude 串起来，并看剩余额度。",
			Sub:      "想完全自控就自托管 · 运维嫌重就用云 · 云端目前免费",
			Trust:    `llms 由 <a href="https://goodtek.xyz/" target="_blank" rel="noopener noreferrer">goodtek</a> 打造。`,
			CTAStart: "开始使用", CTASignal: "我需要云端",
			WhyTitle:   "为什么用云端中枢",
			Why1Title:  "一个地址",
			Why1Body:   "多个登录捆成一个 URL，还是熟悉的 chat/completions。",
			Why2Title:  "不是代币商城",
			Why2Body:   "调度你已有的席位，并展示剩余额度。",
			Why3Title:  "需要时用云",
			Why3Body:   "想自己跑用 openllms。觉得养机器麻烦，就用托管云 — 目前免费开放。",
			PlanStatus: "云端状态", Loading: "加载中…",
			FreeStart: "开始使用", CLIGuide: "CLI 指南",
			PlansTitle: "云端 · 目前免费", PlansLead: "托管 llms 在谨慎开放期间免费。自建服务器则 openllms 自托管无用量上限。",
			FreeTag:      "托管",
			PayNote:      "如果云端路径对你很重要，请在下方告诉我们。我们用真实需求决定下一步。",
			RouteTitle:   "路由策略",
			RouteLead:    "每个路由 URL 选一种策略。首个 token 到达客户端前就会故障转移 — 不只是选模型。",
			RouteDiff:    "llms 编排你的账号：剩余额度、健康度、请求内故障转移。",
			HonestyTitle: "坦诚",
			HonestyBody:  "仅官方 OAuth 连接你自己的账号。不共享凭证。不伪装 CLI。供应商限额与条款照旧。",
			RouteFTitle:  "failover", RouteFBody: "主账号失败再试备用。",
			RouteFillTitle: "fill-first", RouteFillBody: "先用满一个席位，再切下一个（固定顺序）。",
			RouteBTitle: "balance", RouteBBody: "在健康账号间轮询。",
			RoutePTitle: "prefer-primary", RoutePBody: "主席约 80/20 优先。",
			RouteQTitle: "quota-first", RouteQBody: "优先剩余更多的席位；相同时选更快重置的。",
			RouteRTitle: "reset-soon", RouteRBody: "优先 7d 窗口更快重置的席位。",
			RouteSTitle: "steward", RouteSBody: "剩余×重置紧急度。优先 OAuth 席；付费 API key 放 fallback_slugs。",
			HowTitle: "如何开通", HowLead: "安装后在 CLI 登录并打开免费云端：",
			Note:      "账号与路由：llms add · llms status · llms plan · llms signal。",
			NeedAuth:  "先在 CLI 登录：llms login · 然后 llms free，或在此告诉我们需要云端。",
			Unlimited: "不限", None: "无", Yes: "是", No: "否",
			Plan: "套餐", Status: "状态", Entitled: "可用",
			Limits: "限额", Usage: "用量",
			Accounts: "账户", Routes: "路由", Keys: "密钥", PeriodSuffix: "/ 月",
			FreeFail: "无法开通免费云端", FreeOK: "已开通免费云端。",
			FreeFeat1: "起步：账户 1 · 路由 1 · 密钥 1", FreeFeat2: "托管运维 — 不用养机器", FreeFeat3: "路由与剩余额度看板",
			CrewKicker: "告诉我们", CrewTitle: "当你需要云端",
			CrewLead:      "自托管觉得重，或托管中枢已经影响你怎么干活 — 一键告诉 goodtek 与 VibeCrew。我们据此决定下一步。",
			CrewWeek:      "人本周表示需要云端",
			CrewTotal:     "累计：{n}",
			CrewNoteLabel: "可选备注（你在做什么）",
			CrewNotePH:    "例如：用 Cursor 做机器人…",
			CrewCTA:       "我需要云端",
			CrewBrands:    "由",
			CrewOK:        "收到 — 谢谢。计数刚动了。",
			CrewFail:      "发送失败",
		}
	default:
		return billingCopy{
			Lang: "ko", Title: "llms — 클라우드 (지금은 무료)", Skip: "본문으로",
			Kicker: "goodtek 클라우드", H1: "구독은 그대로, API는 하나.",
			Lead:     "Cursor·IDE·봇에는 OpenAI 호환 주소 하나만. 이미 내고 있는 ChatGPT·Claude 계정으로 라우팅하고 남은 한도를 봅니다.",
			Sub:      "직접 돌리고 싶으면 셀프호스트 · 운영이 부담이면 클라우드 · 클라우드는 지금은 무료",
			Trust:    `llms는 <a href="https://goodtek.xyz/" target="_blank" rel="noopener noreferrer">goodtek</a>이 만듭니다.`,
			CTAStart: "시작하기", CTASignal: "클라우드가 필요해요",
			WhyTitle:   "왜 클라우드 허브인가요",
			Why1Title:  "주소 하나",
			Why1Body:   "계정이 여러 개여도 Cursor·IDE·봇에는 URL 하나만. 익숙한 chat/completions 그대로.",
			Why2Title:  "토큰 마켓이 아닙니다",
			Why2Body:   "이미 가진 구독·키를 묶고, 남은 한도를 보면서 나눕니다.",
			Why3Title:  "필요할 때의 클라우드",
			Why3Body:   "직접 돌리려면 openllms. 서버 운영이 부담되면 호스팅 클라우드 — 지금은 무료로 열어 둡니다.",
			PlanStatus: "클라우드 상태", Loading: "불러오는 중…",
			FreeStart: "시작하기", CLIGuide: "CLI 가이드",
			PlansTitle: "클라우드 · 지금은 무료", PlansLead: "호스팅 llms는 조심스레 여는 동안 무료예요. 내 서버면 openllms 셀프호스트는 한도 없이 쓸 수 있어요.",
			FreeTag:      "호스팅",
			PayNote:      "클라우드 경로가 내 작업에 필요하다고 느끼면 아래에 알려 주세요. 다음에 뭘 만들지 판단하는 신호가 됩니다.",
			RouteTitle:   "라우팅 정책",
			RouteLead:    "라우트 URL마다 정책을 고릅니다. 첫 토큰이 클라이언트에 닿기 전에 페일오버 — 모델 목록만 고르는 게 아닙니다.",
			RouteDiff:    "llms는 내 계정을 오케스트레이션합니다 — 잔여량·상태·요청 중 페일오버.",
			HonestyTitle: "정직하게",
			HonestyBody:  "공식 OAuth로 내 계정만 연결합니다. 자격증명 공유 없음. CLI 위장 없음. 프로바이더 한도와 ToS는 그대로입니다.",
			RouteFTitle:  "failover", RouteFBody: "주 계정 다음 백업 순으로 시도.",
			RouteFillTitle: "fill-first", RouteFillBody: "한 좌석을 먼저 쓰고, 막히면 다음 (고정 순서).",
			RouteBTitle: "balance", RouteBBody: "건강한 계정에 라운드로빈.",
			RoutePTitle: "prefer-primary", RoutePBody: "주 좌석을 대략 80/20으로 우선.",
			RouteQTitle: "quota-first", RouteQBody: "남은 한도가 더 많은 좌석을 우선. 동률이면 리셋이 빠른 쪽.",
			RouteRTitle: "reset-soon", RouteRBody: "7d 창 기준 리셋이 가까운 좌석부터 소진.",
			RouteSTitle: "steward", RouteSBody: "잔여×리셋 긴급도. OAuth 좌석 우선. 유료 API 키는 fallback_slugs.",
			HowTitle: "활성화", HowLead: "설치 후 CLI에서 로그인하고 무료 클라우드를 켜세요:",
			Note:      "계정·라우트: llms add · llms status · llms plan · llms signal.",
			NeedAuth:  "먼저 CLI에서 로그인: llms login · 그다음 llms free, 또는 여기서 클라우드가 필요하다고 알려 주세요.",
			Unlimited: "무제한", None: "없음", Yes: "예", No: "아니오",
			Plan: "플랜", Status: "상태", Entitled: "이용 가능",
			Limits: "한도", Usage: "사용량",
			Accounts: "계정", Routes: "라우트", Keys: "키", PeriodSuffix: "/ 월",
			FreeFail: "무료 클라우드 시작 실패", FreeOK: "무료 클라우드가 활성화되었습니다.",
			FreeFeat1: "시작: 계정 1 · 라우트 1 · 키 1", FreeFeat2: "호스팅 운영 — 서버 돌볼 필요 없음", FreeFeat3: "라우팅·남은 한도 보드",
			CrewKicker: "알려 주세요", CrewTitle: "클라우드가 필요할 때",
			CrewLead:      "셀프호스트가 부담되거나, 호스팅 허브가 이미 일하는 방식에 필요하면 — 한 번 눌러 goodtek와 VibeCrew에 알려 주세요. 다음에 뭘 만들지 판단하는 신호가 됩니다.",
			CrewWeek:      "명이 이번 주 클라우드가 필요하다고 했어요",
			CrewTotal:     "누적: {n}",
			CrewNoteLabel: "선택 메모 (무엇을 만들고 있나요)",
			CrewNotePH:    "예: Cursor로 봇 만드는 중…",
			CrewCTA:       "클라우드가 필요해요",
			CrewBrands:    "제작",
			CrewOK:        "받았어요 — 고마워요. 카운터가 움직였습니다.",
			CrewFail:      "전송 실패",
		}
	}
}

func serveBillingHTML(w http.ResponseWriter, r *http.Request, lang string) {
	c := billingCopyFor(lang)
	if c.PeriodSuffix == "" {
		switch lang {
		case "en":
			c.PeriodSuffix = "/ mo"
		case "ja", "zh":
			c.PeriodSuffix = "/ 月"
		default:
			c.PeriodSuffix = "/ 월"
		}
	}
	raw, err := fs.ReadFile(publicStatic, "static/billing.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	js, _ := json.Marshal(c)
	out := string(raw)
	repl := map[string]string{
		"{{BILLING_LANG}}":             c.Lang,
		"{{BILLING_TITLE}}":            c.Title,
		"{{BILLING_SKIP}}":             c.Skip,
		"{{BILLING_KICKER}}":           c.Kicker,
		"{{BILLING_H1}}":               c.H1,
		"{{BILLING_LEAD}}":             c.Lead,
		"{{BILLING_SUB}}":              c.Sub,
		"{{BILLING_TRUST}}":            c.Trust,
		"{{BILLING_CTA_START}}":        c.CTAStart,
		"{{BILLING_CTA_SIGNAL}}":       c.CTASignal,
		"{{BILLING_WHY_TITLE}}":        c.WhyTitle,
		"{{BILLING_WHY1_TITLE}}":       c.Why1Title,
		"{{BILLING_WHY1_BODY}}":        c.Why1Body,
		"{{BILLING_WHY2_TITLE}}":       c.Why2Title,
		"{{BILLING_WHY2_BODY}}":        c.Why2Body,
		"{{BILLING_WHY3_TITLE}}":       c.Why3Title,
		"{{BILLING_WHY3_BODY}}":        c.Why3Body,
		"{{BILLING_PLAN_STATUS}}":      c.PlanStatus,
		"{{BILLING_LOADING}}":          c.Loading,
		"{{BILLING_FREE_START}}":       c.FreeStart,
		"{{BILLING_CLI_GUIDE}}":        c.CLIGuide,
		"{{BILLING_PLANS_TITLE}}":      c.PlansTitle,
		"{{BILLING_PLANS_LEAD}}":       c.PlansLead,
		"{{BILLING_FREE_TAG}}":         c.FreeTag,
		"{{BILLING_PAY_NOTE}}":         c.PayNote,
		"{{BILLING_ROUTE_TITLE}}":      c.RouteTitle,
		"{{BILLING_ROUTE_LEAD}}":       c.RouteLead,
		"{{BILLING_ROUTE_DIFF}}":       c.RouteDiff,
		"{{BILLING_HONESTY_TITLE}}":    c.HonestyTitle,
		"{{BILLING_HONESTY_BODY}}":     c.HonestyBody,
		"{{BILLING_ROUTE_F_TITLE}}":    c.RouteFTitle,
		"{{BILLING_ROUTE_F_BODY}}":     c.RouteFBody,
		"{{BILLING_ROUTE_FILL_TITLE}}": c.RouteFillTitle,
		"{{BILLING_ROUTE_FILL_BODY}}":  c.RouteFillBody,
		"{{BILLING_ROUTE_B_TITLE}}":    c.RouteBTitle,
		"{{BILLING_ROUTE_B_BODY}}":     c.RouteBBody,
		"{{BILLING_ROUTE_P_TITLE}}":    c.RoutePTitle,
		"{{BILLING_ROUTE_P_BODY}}":     c.RoutePBody,
		"{{BILLING_ROUTE_Q_TITLE}}":    c.RouteQTitle,
		"{{BILLING_ROUTE_Q_BODY}}":     c.RouteQBody,
		"{{BILLING_ROUTE_R_TITLE}}":    c.RouteRTitle,
		"{{BILLING_ROUTE_R_BODY}}":     c.RouteRBody,
		"{{BILLING_ROUTE_S_TITLE}}":    c.RouteSTitle,
		"{{BILLING_ROUTE_S_BODY}}":     c.RouteSBody,
		"{{BILLING_HOW_TITLE}}":        c.HowTitle,
		"{{BILLING_HOW_LEAD}}":         c.HowLead,
		"{{BILLING_NOTE}}":             c.Note,
		"{{BILLING_FREE_F1}}":          c.FreeFeat1,
		"{{BILLING_FREE_F2}}":          c.FreeFeat2,
		"{{BILLING_FREE_F3}}":          c.FreeFeat3,
		"{{BILLING_PERIOD}}":           c.PeriodSuffix,
		"{{BILLING_CREW_KICKER}}":      c.CrewKicker,
		"{{BILLING_CREW_TITLE}}":       c.CrewTitle,
		"{{BILLING_CREW_LEAD}}":        c.CrewLead,
		"{{BILLING_CREW_WEEK}}":        c.CrewWeek,
		"{{BILLING_CREW_TOTAL}}":       c.CrewTotal,
		"{{BILLING_CREW_NOTE_LABEL}}":  c.CrewNoteLabel,
		"{{BILLING_CREW_NOTE_PH}}":     c.CrewNotePH,
		"{{BILLING_CREW_CTA}}":         c.CrewCTA,
		"{{BILLING_CREW_BRANDS}}":      c.CrewBrands,
		"{{BILLING_I18N_JSON}}":        string(js),
		"{{BILLING_HREF}}":             billingPathForLang(lang),
		"{{INSTALL_HREF}}":             installPathForLang(lang),
	}
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	serveChromeHTMLBytes(w, r, out, "billing", lang, "")
}
