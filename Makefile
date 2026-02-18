POSTGRES_URL ?= postgres://postgres:postgres@localhost:5432/mixpost_go?sslmode=disable

.PHONY: deps fmt test run-api run-worker run-scheduler compose-up compose-down compose-logs migrate-up migrate-down

deps:
	go mod tidy

fmt:
	go fmt ./...

test:
	go test ./...

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker

run-scheduler:
	go run ./cmd/scheduler

compose-up:
	docker compose up -d

compose-down:
	docker compose down

compose-logs:
	docker compose logs -f

migrate-up:
	@for file in $$(ls migrations/*.up.sql | sort); do \
		echo "Applying $$file"; \
		psql "$(POSTGRES_URL)" -v ON_ERROR_STOP=1 -f $$file; \
	done

migrate-down:
	@for file in $$(ls migrations/*.down.sql | sort -r); do \
		echo "Reverting $$file"; \
		psql "$(POSTGRES_URL)" -v ON_ERROR_STOP=1 -f $$file; \
	done
