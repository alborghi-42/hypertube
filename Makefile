.PHONY: dev down migrate-up migrate-down sqlc test lint build-image

DATABASE_URL ?= postgres://hypertube:hypertube@localhost:5432/hypertube?sslmode=disable

dev: ## Start the full dev stack (postgres + backend hot-reload + ng serve)
	docker compose up --build

down:
	docker compose down

migrate-up: ## Requires golang-migrate CLI
	migrate -path backend/migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path backend/migrations -database "$(DATABASE_URL)" down 1

sqlc: ## Regenerate the repository package from db/queries
	cd backend && sqlc generate

test:
	cd backend && go vet ./... && go test ./...
	cd frontend && npx ng test --watch=false

lint:
	cd backend && gofmt -l . && go vet ./...

build-image:
	docker build -t hypertube:local .
