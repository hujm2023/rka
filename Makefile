# Repository orchestration; each application owns its dependency/tool manifests.
# Resolve local runtime paths before delegating to backend/.
override export RKA_DATA_DIR := $(abspath $(or $(RKA_DATA_DIR),data))
override export RKA_STATIC_DIR := $(abspath $(or $(RKA_STATIC_DIR),frontend/dist))
override export RKA_CONTENT_SNAPSHOT := $(abspath $(or $(RKA_CONTENT_SNAPSHOT),content))
ifneq ($(strip $(RKA_SECRET_KEY_FILE)),)
override export RKA_SECRET_KEY_FILE := $(abspath $(RKA_SECRET_KEY_FILE))
endif

.PHONY: help build vet test test-integration test-platform test-legacy fmt web frontend-install frontend-dev frontend-test typecheck dev-db migrate serve worker init import-content content backup verify-backup restore retention doctor compose-up compose-down compose-logs e2e

help:
	@echo "build / vet / test / test-platform / fmt — delegate to backend/"
	@echo "frontend-install / frontend-dev / web / frontend-test / typecheck — frontend/"
	@echo "dev-db / init / serve / worker / migrate / import-content — local development"
	@echo "content — rebuild the shared content snapshot from sibling source projects"
	@echo "e2e — build the browser bundle and run Playwright against Go"
	@echo "backup / verify-backup / restore / retention / doctor — operations"
	@echo "compose-up / compose-down / compose-logs — whole-platform Docker Compose"

build vet test test-integration test-platform test-legacy fmt migrate serve worker init import-content verify-backup restore retention doctor:
	$(MAKE) -C backend $@ ARGS="$(ARGS)"

frontend-install:
	npm --prefix frontend ci

frontend-dev:
	npm --prefix frontend run dev

web:
	npm --prefix frontend run build

frontend-test:
	npm --prefix frontend test

typecheck:
	npm --prefix frontend run typecheck

dev-db:
	scripts/dev-db.sh

content:
	node scripts/import-content.mjs

backup:
	scripts/backup.sh $(ARGS)

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

compose-logs:
	docker compose logs -f app worker

e2e: web
	npm --prefix frontend run test:e2e
