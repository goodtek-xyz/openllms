.PHONY: test tidy build run health dev-db dev-db-down

export PATH := /usr/local/go/bin:$(PATH)

test:
	go test ./...

tidy:
	go mod tidy

build:
	go build -o bin/llms-gateway ./cmd/llms-gateway
	go build -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo dev) -X main.Commit=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" -o bin/llms ./cmd/llms

.PHONY: build-cli-dist
build-cli-dist:
	bash scripts/build-cli-dist.sh $${OUT:-./data/dist}

run: build
	DATABASE_URL=$${DATABASE_URL:-postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable} \
	HTTP_ADDR=$${HTTP_ADDR:-:8080} \
	./bin/llms-gateway

health:
	curl -sf http://127.0.0.1:8080/health
	@echo
	curl -sf http://127.0.0.1:8080/ready
	@echo

# Local Postgres for `make run` (deploy/dev/compose.yaml, 127.0.0.1:54329).
dev-db:
	docker compose -f deploy/dev/compose.yaml up -d --wait

dev-db-down:
	docker compose -f deploy/dev/compose.yaml down

.PHONY: build-oss build-cloud

# Default binary: public-first OSS (file secrets; no cloud/)
build-oss:
	go build -o bin/llms-gateway ./cmd/llms-gateway
	go build -o bin/llms ./cmd/llms

# Cloud binary: Cloud overlay (Infisical + admin/billing)
build-cloud:
	go build -tags cloud -o bin/llms-gateway-cloud ./cmd/llms-gateway

