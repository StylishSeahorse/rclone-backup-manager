.PHONY: test build css docker up down

VERSION ?= dev

test:
	go vet ./...
	go test -race -count=1 ./...
	@if command -v node >/dev/null; then node web/schedule.test.js; else echo "node not found: skipping web/schedule.test.js"; fi

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/server ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/agent ./cmd/agent

# Regenerates the committed stylesheet; the Docker build does this itself.
css:
	cd web && npm ci && npm run build

docker:
	docker build -f docker/Dockerfile.server --build-arg VERSION=$(VERSION) -t wasabi-backup/server:latest .
	docker build -f docker/Dockerfile.agent  --build-arg VERSION=$(VERSION) -t wasabi-backup/agent:latest .

up:
	docker compose up -d --build

down:
	docker compose down
