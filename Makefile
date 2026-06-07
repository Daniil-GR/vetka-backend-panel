.PHONY: run test migrate-up migrate-down fmt

run:
	go run ./cmd/server

test:
	go test ./...

migrate-up:
	go run ./cmd/server --migrate-up

migrate-down:
	@echo "migrate-down is not implemented for the MVP"

fmt:
	go fmt ./...
