.PHONY: build-gateway build-message build-sync migrate-up migrate-down test

build-gateway:
	go build -o bin/gateway ./cmd/gateway

build-message:
	go build -o bin/message ./cmd/message

build-sync:
	go build -o bin/sync ./cmd/sync

build-all: build-gateway build-message build-sync

migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)

test:
	go test ./... -v -count=1

test-short:
	go test ./... -v -short -count=1

.DEFAULT_GOAL := build-all
