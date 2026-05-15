.PHONY: migrate-up migrate-down migrate-down-one migrate-create migrate-version migrate-force seed-env seed-env-local

DB_URL="postgres://spacetrek:***REMOVED***@psql:5432/spacetrek?sslmode=disable"

migrate-up:
	@echo "Running migrations up..."
	docker compose run --rm migrate -path /migrations -database "$(DB_URL)" up

migrate-down:
	@echo "Running migrations down..."
	docker compose run --rm migrate -path /migrations -database "$(DB_URL)" down

migrate-down-one:
	@echo "Rolling back 1 migration..."
	docker compose run --rm migrate -path /migrations -database "$(DB_URL)" down 1

migrate-create:
	@echo "Creating new migration: $(NAME)"
	docker compose run --rm migrate create -ext sql -dir /migrations -seq $(NAME)

migrate-version:
	@echo "Current migration version:"
	docker compose run --rm migrate -path /migrations -database "$(DB_URL)" version

migrate-force:
	@echo "Forcing version to $(VERSION)..."
	docker compose run --rm migrate -path /migrations -database "$(DB_URL)" force $(VERSION)

seed-env:
	@echo "Seeding environments from JSON (docker compose)..."
	docker compose run --rm --entrypoint /app/seed api

seed-env-local:
	@echo "Seeding environments from JSON (local)..."
	go run ./cmd/seed
