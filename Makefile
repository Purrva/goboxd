.PHONY: build run run-detach stop test test-local integration lint lint-local load clean healthz readyz info help

# Configuration Variables
IMAGE_NAME     ?= goboxd:stage1
CONTAINER_NAME ?= goboxd-instance
PORT           ?= 8080
VERSION        ?= 1.0.0-stage1
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

help: ## Show this help menu
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the secure multi-stage Docker image using local submodules
	@echo "Checking git submodule status..."
	@git submodule update --init --recursive
	@echo "Building secure container stage image..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE_NAME) .

run: ## Run the container interactively in the foreground (Requires Privileged for nsjail)
	@echo "Launching goboxd infrastructure on port $(PORT)..."
	docker run --rm --name $(CONTAINER_NAME) \
		--privileged \
		-p $(PORT):8080 \
		$(IMAGE_NAME)

run-detach: ## Start the service safely in the background
	@echo "Launching goboxd in background..."
	docker run -d --name $(CONTAINER_NAME) \
		--privileged \
		-p $(PORT):8080 \
		$(IMAGE_NAME)

stop: ## Stop the background running container
	@echo "Stopping goboxd instance..."
	docker stop $(CONTAINER_NAME) 2>/dev/null || true

test: ## Run local unit test suites safely with data race detection
	@echo "Running local unit test suites..."
	go test -v -race ./internal/...

test-local: ## Run quick unit tests locally
	go test ./internal/... -v -count=1

integration: stop build ## Build and execute end-to-end integration tests
	@echo "Starting detached service for integration verification..."
	docker run -d --name $(CONTAINER_NAME) --privileged -p $(PORT):8080 $(IMAGE_NAME)
	@echo "Waiting for service availability probes..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$(PORT)/healthz > /dev/null 2>&1; then break; fi; \
		sleep 1; \
	done
	@echo "Executing end-to-end verification tests..."
	go test ./tests/integration/... -v -count=1 -timeout 120s
	@$(MAKE) stop

lint: ## Run static analysis checks inside a temporary image build target
	docker build --target go-builder -t goboxd-builder:dev .
	docker run --rm goboxd-builder:dev golangci-lint run ./...

lint-local: ## Run local linter if present in system path
	golangci-lint run ./...

load: ## Run a traffic load simulation test using hey
	@echo "Running load test against http://localhost:$(PORT)/run"
	@which hey > /dev/null 2>&1 || (echo "Install error: go install github.com/rakyll/hey@latest" && exit 1)
	hey -n 200 -c 10 -m POST \
		-H "Content-Type: application/json" \
		-d '{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_stdout":"hello\n"}]}' \
		http://localhost:$(PORT)/run

clean: stop ## Purge lingering verification images and test containers
	docker image rm -f $(IMAGE_NAME) goboxd-builder:dev 2>/dev/null || true

healthz: ## Check engine endpoint liveness
	curl -sf http://localhost:$(PORT)/healthz | python3 -m json.tool

readyz: ## Check system runtime component readiness
	curl -sf http://localhost:$(PORT)/readyz | python3 -m json.tool || true

info: ## Show runtime instance metadata
	curl -sf http://localhost:$(PORT)/info | python3 -m json.tool