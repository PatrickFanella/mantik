SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.NOTPARALLEL: verify

.PHONY: help verify fmt lint test compose-config open-pilot-check

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: fmt-check lint test build compose-config open-pilot-check pilot-check backtesting-contract-check recovery-contract-check release-preflight-check rehearsal-check ## Run all checks

fmt: ## Format Go files
	@find cmd internal migrations scripts/testshard -name '*.go' -print0 | xargs -0 -r gofmt -w

lint: ## Run Go vet
	go vet ./...

test: ## Run Go tests
	go test ./...

compose-config: ## Validate Docker Compose if present
	@if [ -f docker-compose.yml ]; then docker compose config --quiet; else echo 'skip compose config'; fi

open-pilot-check: ## Check all issue forms and label definitions
	@set -e; for form in task bug feature docs ready; do \
		test -f .gitea/ISSUE_TEMPLATE/open-pilot-$$form.yaml; \
		grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-$$form.yaml; \
	done
	@grep -q 'status:needs-review' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@! grep -Eq '^ *- ready-for-agent$$|^ *- agent:queued$$' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'agent:queued' .gitea/ISSUE_TEMPLATE/open-pilot-ready.yaml
	@test -f docs/reference/open-pilot-labels.yaml

.PHONY: fmt-check
fmt-check: ## Check formatting without changing sources
	@set -e; unformatted=$$(find cmd internal migrations scripts/testshard -name '*.go' -print0 | xargs -0 -r gofmt -l); \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

.PHONY: compose-build
compose-build: ## Build local container images without starting services
	docker compose build

.PHONY: build demo test-race test-race-other test-integration
build: ## Build the Mantik CLI/service
	go build -trimpath -o bin/mantik ./cmd/mantik

demo: build ## Run the synthetic paper lifecycle without dependencies
	./bin/mantik demo

# Storage exceeds one package alarm on slower shared runners. Every case still
# runs once, with race detection and a separate 30-minute alarm per group.
test-race: test-race-other ## Verify concurrent operation with the race detector
	go run ./scripts/testshard -race -parts=2 ./internal/storage

test-race-other: ## Race-check all packages except the grouped storage suite
	@set -e -o pipefail; packages=$$(go list ./... | awk '!/\/internal\/storage$$/'); \
	go test -race -timeout=30m $$packages

test-integration: ## Verify persistence using a new disposable PostgreSQL container
	python3 scripts/test-integration.py

.PHONY: pilot-check
pilot-check: ## Check pilot resource and evidence guards without starting Docker
	python3 -m unittest discover -s scripts -p 'test_pilot.py'

.PHONY: backtesting-contract-check
backtesting-contract-check: ## Check external fixture transport/comparison rules without installing libraries
	python3 -m unittest discover -s research/backtesting -p 'test_adapter.py'

.PHONY: recovery-contract-check
recovery-contract-check: ## Check restored-owner proof and target guards without starting Docker
	python3 -m unittest discover -s scripts -p 'test_recovery_contract.py'

.PHONY: release-preflight-check
release-preflight-check: ## Check source/image preflight guards without starting Docker
	python3 -m unittest discover -s scripts -p 'test_release_preflight.py'

.PHONY: rehearsal-check
rehearsal-check: ## Check isolated model outage and rehearsal cleanup without external services
	python3 -m unittest discover -s scripts -p 'test_rehearsal*.py'
	python3 -m unittest discover -s scripts -p 'test_runtime_rehearsal.py'
