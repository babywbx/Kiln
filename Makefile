.PHONY: build test run tidy hash keys ci audit-admin-ui \
        docker docker-multiarch docker-verify docker-smoke docker-reap fixtures

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT_AT ?= $(shell date -u +%FT%TZ)
IMAGE    ?= kiln:local
PLATFORM ?= linux/amd64,linux/arm64

BUILD_ARGS = --build-arg VERSION=$(VERSION) \
             --build-arg COMMIT=$(COMMIT) \
             --build-arg BUILT_AT=$(BUILT_AT)

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

ci:
	@test -z "$$(gofmt -l .)" || { echo "not gofmt'd:"; gofmt -l .; exit 1; }
	go vet ./...
	go build ./...
	go test -race ./...

audit-admin-ui:
	./scripts/audit-admin-ui.sh $(or $(URL),http://127.0.0.1:8080/admin)

docker:
	docker buildx build -f deploy/docker/Dockerfile $(BUILD_ARGS) -t $(IMAGE) --load .

docker-multiarch:
	docker buildx build -f deploy/docker/Dockerfile $(BUILD_ARGS) \
	  --platform $(PLATFORM) -t $(IMAGE) --output type=cacheonly .

docker-verify:
	docker run --rm --entrypoint /bin/sh -v "$(PWD)/deploy/docker:/d:ro" $(IMAGE) \
	  /d/verify-ffmpeg.sh /usr/local/bin/ffmpeg

docker-smoke:
	@docker network create kilnsmoke >/dev/null 2>&1 || true
	@docker rm -f kiln-origin >/dev/null 2>&1 || true
	docker run -d --name kiln-origin --network kilnsmoke \
	  -v "$(PWD)/testdata/cenc:/www:ro" busybox:1.36 httpd -f -p 8000 -h /www >/dev/null
	-docker run --rm --network kilnsmoke -e ORIGIN_URL=http://kiln-origin:8000 \
	  -v "$(PWD):/src:ro" -w /src --entrypoint /bin/sh $(IMAGE) \
	  deploy/docker/smoke.sh /usr/local/bin/ffmpeg testdata/cenc
	@docker rm -f kiln-origin >/dev/null
	@docker network rm kilnsmoke >/dev/null

docker-reap:
	@docker ps -aq --filter label=kiln.ffmpeg=1 | xargs -r docker rm -f

fixtures:
	./deploy/docker/make-fixtures.sh testdata/cenc
