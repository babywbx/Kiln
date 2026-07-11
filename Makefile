.PHONY: build test run tidy hash keys docker

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

keys:
	@go run scripts/gen-jwt-keys.go $(or $(DIR),./secrets)

docker:
	docker build -f deploy/docker/Dockerfile -t kiln:local .
