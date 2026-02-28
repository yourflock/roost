# Makefile — Roost backend build, test, and Docker targets.
# Run `make help` to list all available targets.

REGISTRY   := ghcr.io/yourflock
IMAGE_TAG  ?= latest
SERVICES   := billing ingest relay catalog epg owl_api sports catchup dvr grid_compositor recommendations vod

.PHONY: help build test build-docker push-docker clean lint vet seed seed-dry seed-iptv seed-podcasts seed-vod seed-games

# ─────────────────────────────────────────────────────────
# Default target
# ─────────────────────────────────────────────────────────

.DEFAULT_GOAL := help

help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ─────────────────────────────────────────────────────────
# Build
# ─────────────────────────────────────────────────────────

build: ## Compile all Go services (validates that the code compiles)
	@echo "Building all services..."
	@go build ./...
	@echo "Build complete."

build-service: ## Build a single service binary — usage: make build-service SERVICE=billing
	@if [ -z "$(SERVICE)" ]; then echo "Usage: make build-service SERVICE=<name>"; exit 1; fi
	@echo "Building $(SERVICE)..."
	@if [ -d "services/$(SERVICE)/cmd/$(SERVICE)" ]; then \
	  go build -o services/$(SERVICE)/bin/$(SERVICE) ./services/$(SERVICE)/cmd/$(SERVICE)/; \
	elif [ -d "cmd/$(SERVICE)" ]; then \
	  go build -o bin/$(SERVICE) ./cmd/$(SERVICE)/; \
	else \
	  go build -o services/$(SERVICE)/bin/$(SERVICE) ./services/$(SERVICE)/...; \
	fi
	@echo "Built services/$(SERVICE)/bin/$(SERVICE)"

# ─────────────────────────────────────────────────────────
# Test
# ─────────────────────────────────────────────────────────

test: ## Run all Go tests with race detector
	@echo "Running all tests..."
	@go test -race -timeout 120s ./...

test-short: ## Run only short tests (no integration / no Docker required)
	@go test -short -timeout 60s ./...

test-coverage: ## Run tests with coverage report (opens HTML report)
	@go test -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ─────────────────────────────────────────────────────────
# Lint & vet
# ─────────────────────────────────────────────────────────

vet: ## Run go vet on all packages
	@go vet ./...
	@echo "go vet passed."

lint: ## Run golangci-lint (must be installed: https://golangci-lint.run/usage/install/)
	@golangci-lint run --timeout=5m ./...

# ─────────────────────────────────────────────────────────
# Docker
# ─────────────────────────────────────────────────────────

build-docker: ## Build Docker images for all services (tag: IMAGE_TAG, default: latest)
	@echo "Building Docker images (tag: $(IMAGE_TAG))..."
	@for svc in $(SERVICES); do \
	  if [ -f "services/$$svc/Dockerfile" ]; then \
	    echo "  Building roost-$$svc:$(IMAGE_TAG)..."; \
	    docker build \
	      -f services/$$svc/Dockerfile \
	      -t $(REGISTRY)/roost-$$svc:$(IMAGE_TAG) \
	      . || exit 1; \
	  fi; \
	done
	@echo "All Docker images built."

build-docker-service: ## Build Docker image for a single service — usage: make build-docker-service SERVICE=billing
	@if [ -z "$(SERVICE)" ]; then echo "Usage: make build-docker-service SERVICE=<name>"; exit 1; fi
	@docker build \
	  -f services/$(SERVICE)/Dockerfile \
	  -t $(REGISTRY)/roost-$(SERVICE):$(IMAGE_TAG) \
	  .
	@echo "Built $(REGISTRY)/roost-$(SERVICE):$(IMAGE_TAG)"

push-docker: ## Push all Docker images to GHCR (must be logged in: docker login ghcr.io)
	@echo "Pushing Docker images (tag: $(IMAGE_TAG))..."
	@for svc in $(SERVICES); do \
	  echo "  Pushing roost-$$svc:$(IMAGE_TAG)..."; \
	  docker push $(REGISTRY)/roost-$$svc:$(IMAGE_TAG) || exit 1; \
	done
	@echo "All images pushed."

push-docker-service: ## Push a single service image — usage: make push-docker-service SERVICE=billing
	@if [ -z "$(SERVICE)" ]; then echo "Usage: make push-docker-service SERVICE=<name>"; exit 1; fi
	@docker push $(REGISTRY)/roost-$(SERVICE):$(IMAGE_TAG)

# ─────────────────────────────────────────────────────────
# Clean
# ─────────────────────────────────────────────────────────

# ─────────────────────────────────────────────────────────
# Seed (development only)
# ─────────────────────────────────────────────────────────

seed: ## Seed sample content into the local dev database (POSTGRES_URL must be set)
	@echo "Seeding sample content..."
	@go run ./cmd/seed
	@echo "Seed complete."

seed-dry: ## Print seed plan without writing to database
	@go run ./cmd/seed --dry-run

seed-iptv: ## Seed IPTV channels only
	@go run ./cmd/seed --only=iptv

seed-podcasts: ## Seed podcast feeds only
	@go run ./cmd/seed --only=podcasts

seed-vod: ## Seed VOD catalog entries only
	@go run ./cmd/seed --only=vod

seed-games: ## Seed game ROMs from ROM_SEED_DIR
	@if [ -z "$(ROM_SEED_DIR)" ]; then \
	  echo "Usage: make seed-games ROM_SEED_DIR=/path/to/roms"; exit 1; \
	fi
	@ROM_SEED_DIR=$(ROM_SEED_DIR) go run ./cmd/seed --only=games

# ─────────────────────────────────────────────────────────
# Clean
# ─────────────────────────────────────────────────────────

clean: ## Remove compiled binaries and test artifacts
	@echo "Cleaning build artifacts..."
	@rm -f coverage.out coverage.html
	@find . -path '*/bin/*' -not -path '*/vendor/*' -type f -delete 2>/dev/null || true
	@go clean ./...
	@echo "Clean complete."
