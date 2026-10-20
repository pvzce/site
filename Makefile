GO ?= go
VERSION ?= dev
COMPOSE ?= docker compose

.PHONY: run build fmt vet test check integration docker-build auth-init up down db-up db-down

run:
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; $(GO) run ./cmd/server

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w -X pvzce-site/internal/buildinfo.Version=$(VERSION)" -o bin/server ./cmd/server

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

check: vet test

integration:
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; test -n "$$TEST_DATABASE_URL" || { echo 'Set TEST_DATABASE_URL to a test database'; exit 1; }; $(GO) test -race ./internal/database ./internal/server ./internal/auth ./internal/migrate ./internal/workshop ./internal/gamelogin ./internal/httpapi

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t pvzce-site:$(VERSION) .

auth-init: docker-build
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$$(pwd):/workspace" -w /workspace pvzce-site:$(VERSION) init-casdoor

up: auth-init
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

db-up:
	$(COMPOSE) -f compose.yaml -f compose.dev.yaml up -d database

db-down:
	$(COMPOSE) -f compose.yaml -f compose.dev.yaml stop database

workshop-init:
	$(GO) run ./cmd/server init-casdoor
	python3 scripts/init-workshop.py

workshop-up:
	$(COMPOSE) -f compose.yaml -f compose.dev.yaml -f compose.workshop.yaml up -d database casdoor s3 scanner
