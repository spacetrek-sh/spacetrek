.PHONY: migrate-up migrate-down migrate-down-one migrate-create migrate-version migrate-force seed-env seed-env-local

COMPOSE := docker compose --profile migrate

migrate-up:
	@echo "Running migrations up..."
	$(COMPOSE) run --rm migrate up

migrate-down:
	@echo "Running migrations down..."
	$(COMPOSE) run --rm migrate down

migrate-down-one:
	@echo "Rolling back 1 migration..."
	$(COMPOSE) run --rm migrate down 1

migrate-create:
	@echo "Creating new migration: $(NAME)"
	$(COMPOSE) run --rm migrate create -ext sql -dir /migrations -seq $(NAME)

migrate-version:
	@echo "Current migration version..."
	$(COMPOSE) run --rm migrate version

migrate-force:
	@echo "Forcing version to $(VERSION)..."
	$(COMPOSE) run --rm migrate force $(VERSION)

seed-env:
	@echo "Seeding environments from JSON (docker compose)..."
	docker compose run --rm --entrypoint /app/seed api

seed-env-local:
	@echo "Seeding environments from JSON (local)..."
	go run ./cmd/seed
