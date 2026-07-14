.PHONY: migrate-up migrate-down migrate-down-one migrate-create migrate-version migrate-force seed-env seed-env-local rebuild-api rebuild-activator rebuild-mesh-activator

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

# Rebuild spacetrek-api AND both activators together. The activators use
# network_mode: "service:api", which resolves to a specific container ID at
# creation time; if you rebuild api alone, the activators are left pointing
# at the old (deleted) api container and silently die. Always rebuild all
# three so the netns reference stays live.
rebuild-api:
	@echo "Rebuilding api + activators (activators share api's netns)..."
	docker compose up -d --build --force-recreate api activator mesh-activator

# Recover from a stale activator after an api rebuild that didn't include it.
rebuild-activator:
	@echo "Force-recreating activator to rejoin current api netns..."
	docker compose up -d --force-recreate activator

# Recover from a stale mesh-activator after an api rebuild that didn't
# include it. Same netns-coupling issue as the public activator.
rebuild-mesh-activator:
	@echo "Force-recreating mesh-activator to rejoin current api netns..."
	docker compose up -d --force-recreate mesh-activator
