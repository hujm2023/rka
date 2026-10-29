# Go module commands. Shared data/content/assets remain at the repository root.
GO_PKGS := ./...
GOFLAGS_LOCAL := -trimpath
export RKA_DATA_DIR ?= $(abspath ../data)
export RKA_STATIC_DIR ?= $(abspath ../frontend/dist)
export RKA_CONTENT_SNAPSHOT ?= $(abspath ../content)
export RKA_TEST_DATABASE_URL ?= postgres://rka:devpassword@127.0.0.1:55432/postgres?sslmode=disable

.PHONY: help build vet test test-integration test-platform test-legacy fmt migrate serve worker init import-content verify-backup restore retention doctor

help:
	@echo "build / vet / test / test-platform / fmt — Go module checks"
	@echo "init / migrate / serve / worker / import-content — local application"
	@echo "verify-backup / restore / retention / doctor — operations (ARGS supported)"

build:
	go build $(GOFLAGS_LOCAL) -o bin/rka ./cmd/rka

vet:
	go vet $(GO_PKGS)

test test-integration:
	go test -p 1 -count=1 $(GO_PKGS)

test-platform:
	go test -p 1 -count=1 ./internal/platform/...

test-legacy:
	node --test legacy/ai-handler.test.mjs

fmt:
	gofmt -w cmd internal

migrate serve worker init import-content verify-backup restore retention doctor:
	go run ./cmd/rka $@ $(ARGS)
