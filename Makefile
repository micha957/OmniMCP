.PHONY: run test vet build docker-up

run:
	go run ./cmd/omnimcp

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -o bin/omnimcp ./cmd/omnimcp

docker-up:
	docker compose up --build -d
