GO ?= go

.PHONY: build demo test vet check smoke dist

build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/termfana ./cmd/termfana

demo: build
	./bin/termfana demo

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: test vet

smoke: build
	python3 scripts/pty_smoke.py

dist:
	@mkdir -p dist
	@for os in linux darwin; do \
		for arch in amd64 arm64; do \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags='-s -w' \
				-o dist/termfana-$$os-$$arch ./cmd/termfana || exit 1; \
		done; \
	done
