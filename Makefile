.PHONY: build build-debug build-release build-lite test test-lite test-extended test-admin-ui test-docker-targets test-lite-contract test-local-tools test-complete coverage run tidy hash keys fmt vet lint vuln ci clean audit-admin-ui \
		docker docker-full docker-core docker-lite docker-images docker-multiarch docker-core-multiarch docker-lite-multiarch \
		docker-verify docker-verify-images docker-verify-lite docker-smoke docker-smoke-lite docker-reap fixtures \
        media-oracle test-safety test-resource-docker-basic test-resource-docker-extended benchmark-performance performance-live soak

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT_AT ?= $(shell date -u +%FT%TZ)
IMAGE    ?= kiln:local
CORE_IMAGE ?= kiln:core-local
LITE_IMAGE ?= kiln:lite-local
BUSYBOX_IMAGE ?= busybox:1.37.0@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028
PLATFORM ?= linux/amd64,linux/arm64
CORE_PLATFORM ?= linux/amd64,linux/arm64,linux/arm/v7,linux/arm/v6
LITE_PLATFORM ?= linux/amd64,linux/arm64

BUILD_ARGS = --build-arg VERSION=$(VERSION) \
             --build-arg COMMIT=$(COMMIT) \
             --build-arg BUILT_AT=$(BUILT_AT)

LDFLAGS = -X github.com/babywbx/kiln/modules/version.Version=$(VERSION) \
          -X github.com/babywbx/kiln/modules/version.Commit=$(COMMIT) \
          -X github.com/babywbx/kiln/modules/version.BuiltAt=$(BUILT_AT)

build: build-debug

build-debug:
	@mkdir -p dist
	go build -o dist/kiln -ldflags="$(LDFLAGS)" ./apps/server

build-release:
	@mkdir -p dist
	go build -trimpath -o dist/kiln -ldflags="-s -w $(LDFLAGS)" ./apps/server

build-lite:
	@mkdir -p dist
	CGO_ENABLED=0 go build -tags=lite -trimpath -o dist/kiln-lite \
	  -ldflags="-s -w -buildid= $(LDFLAGS)" ./apps/server

test:
	go test -race ./...

test-extended:
	go test -race -tags=extended ./...

test-admin-ui:
	node --test scripts/*.test.js

test-docker-targets:
	sh deploy/docker/go-target-env.test.sh
	sh deploy/docker/resource-profile-smoke.test.sh basic

test-lite-contract: build-lite
	scripts/lite-contract.test.sh dist/kiln-lite

test-lite: test-lite-contract
	go test -race -tags=lite ./...

test-local-tools:
	sh deploy/docker/resource-profile-smoke.test.sh extended
	sh scripts/live-performance.test.sh
	sh scripts/test-tiering.test.sh

test-resource-docker-basic:
	sh deploy/docker/resource-profile-smoke.sh basic "$(CORE_IMAGE)"

test-resource-docker-extended:
	sh deploy/docker/resource-profile-smoke.sh extended "$(CORE_IMAGE)"

media-oracle:
	KILN_REQUIRE_MEDIA_ORACLE=1 go test ./modules/packager/... -run 'FFmpeg|NativeOutput'

test-safety:
	go test -tags=extended ./modules/packager/mpd -run '^FuzzAvailableSegments$$'
	go test -tags=extended ./modules/packager/cmaf -run '^FuzzCMAF$$'
	go test -tags=extended ./modules/packager/mpd -run '^$$' -fuzz '^FuzzAvailableSegments$$' -fuzztime=30s
	go test -tags=extended ./modules/packager/cmaf -run '^$$' -fuzz '^FuzzCMAF$$' -fuzztime=30s

benchmark-performance:
	go test -tags=extended ./modules/packager/... ./modules/epg ./modules/httpserver -run '^$$' -bench . -benchmem

performance-live:
	sh scripts/live-performance.sh

soak:
	go run ./apps/soak -server "$(or $(SOAK_SERVER),http://127.0.0.1:8080)" \
		-duration "$(or $(SOAK_DURATION),24h)" $(SOAK_ARGS)

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

tidy:
	go mod tidy

run:
	go run ./apps/server -config configs/examples/kiln.toml

hash:
	@go run scripts/hash-password.go $(PASSWORD)

keys:
	@go run scripts/gen-jwt-keys.go $(or $(DIR),./secrets)

fmt:
	@test -z "$$(gofmt -l .)" || { echo "not gofmt'd:"; gofmt -l .; exit 1; }

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; exit 1; }
	golangci-lint run ./...

vuln:
	@command -v govulncheck >/dev/null 2>&1 || { echo "govulncheck not found: go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1; }
	govulncheck ./...

ci: fmt vet lint build test test-lite test-admin-ui test-docker-targets media-oracle vuln

test-complete: ci test-extended test-local-tools test-safety benchmark-performance
	$(MAKE) docker-core
	$(MAKE) test-resource-docker-extended
	$(MAKE) docker-core-multiarch

clean:
	rm -rf dist coverage.out

audit-admin-ui:
	./scripts/audit-admin-ui.sh $(or $(URL),http://127.0.0.1:8080/admin)

docker: docker-full

docker-full:
	docker buildx build --target full -f deploy/docker/Dockerfile $(BUILD_ARGS) -t $(IMAGE) --load .

docker-core:
	docker buildx build --target core -f deploy/docker/Dockerfile $(BUILD_ARGS) -t $(CORE_IMAGE) --load .

docker-lite:
	docker buildx build --target lite -f deploy/docker/Dockerfile $(BUILD_ARGS) -t $(LITE_IMAGE) --load .

docker-images: docker-lite docker-core docker-full

docker-multiarch:
	docker buildx build --target full -f deploy/docker/Dockerfile $(BUILD_ARGS) \
	  --platform $(PLATFORM) -t $(IMAGE) --output type=cacheonly .

docker-core-multiarch:
	docker buildx build --target core -f deploy/docker/Dockerfile $(BUILD_ARGS) \
	  --platform $(CORE_PLATFORM) -t $(CORE_IMAGE) --output type=cacheonly .

docker-lite-multiarch:
	docker buildx build --target lite -f deploy/docker/Dockerfile $(BUILD_ARGS) \
	  --platform $(LITE_PLATFORM) -t $(LITE_IMAGE) --output type=cacheonly .

docker-verify-images:
	deploy/docker/verify-images.sh $(CORE_IMAGE) $(IMAGE)
	deploy/docker/verify-lite.sh $(LITE_IMAGE)

docker-verify-lite:
	deploy/docker/verify-lite.sh $(LITE_IMAGE)

docker-verify:
	docker run --rm --entrypoint /bin/sh -v "$(PWD)/deploy/docker:/d:ro" $(IMAGE) \
	  /d/verify-ffmpeg.sh /usr/local/bin/ffmpeg

docker-smoke:
	@docker network create kilnsmoke >/dev/null 2>&1 || true
	@docker rm -f kiln-origin >/dev/null 2>&1 || true
	docker run -d --name kiln-origin --network kilnsmoke \
	  -v "$(PWD)/testdata/cenc:/www:ro" $(BUSYBOX_IMAGE) httpd -f -p 8000 -h /www >/dev/null
	-docker run --rm --network kilnsmoke -e ORIGIN_URL=http://kiln-origin:8000 \
	  -v "$(PWD):/src:ro" -w /src --entrypoint /bin/sh $(IMAGE) \
	  deploy/docker/smoke.sh /usr/local/bin/ffmpeg testdata/cenc
	@docker rm -f kiln-origin >/dev/null
	@docker network rm kilnsmoke >/dev/null

docker-smoke-lite:
	deploy/docker/lite-smoke.sh $(LITE_IMAGE) $(BUSYBOX_IMAGE)

docker-reap:
	@docker ps -aq --filter label=kiln.ffmpeg=1 | xargs -r docker rm -f

fixtures:
	./deploy/docker/make-fixtures.sh testdata/cenc
