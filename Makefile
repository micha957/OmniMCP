.PHONY: run agentops test vet build docker-up

run:
	go run ./cmd/omnimcp

agentops:
	go run ./cmd/agentops

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -o bin/omnimcp ./cmd/omnimcp
	go build -o bin/omni ./cmd/omni

docker-up:
	docker compose up --build -d
