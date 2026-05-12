.PHONY: generate lint breaking test tidy build clean sqlc migrate-up migrate-down migrate-status db-up db-down db-test

DB_URL ?= postgres://poker:poker@localhost:5432/poker?sslmode=disable

APP_NAME=pacepoker
CMD_PATH=./cmd/server

generate:
	buf generate

lint:
	buf lint

breaking:
	buf breaking --against '.git#branch=main'

tidy:
	go mod tidy

build: generate
	mkdir -p bin
	go build -o bin/$(APP_NAME) $(CMD_PATH)

test:
	go test ./...

clean:
	rm -rf gen/
	rm -rf bin/

GOBIN = $(shell go env GOPATH)/bin

sqlc:
	cd db && $(GOBIN)/sqlc generate

migrate-up:
	$(GOBIN)/goose -dir db/migrations postgres "$(DB_URL)" up

migrate-down:
	$(GOBIN)/goose -dir db/migrations postgres "$(DB_URL)" down

migrate-status:
	$(GOBIN)/goose -dir db/migrations postgres "$(DB_URL)" status

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

db-test:
	go test ./internal/store/... -count=1