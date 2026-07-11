.PHONY: build test run tidy hash docker

build:
	go build -o dist/kiln ./apps/server

test:
	go test ./...

tidy:
	go mod tidy

run:
	go run ./apps/server -config configs/examples/kiln.toml

hash:
	@go run scripts/hash-password.go $(PASSWORD)

docker:
	docker build -f deploy/docker/Dockerfile -t kiln:local .
