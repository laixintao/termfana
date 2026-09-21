GO ?= go
PYTHON ?= python3
PART ?= patch

.PHONY: build demo test vet check smoke dist package version-check release release-test

build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/termfana ./cmd/termfana

demo: build
	./bin/termfana demo

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: test vet version-check

version-check:
	$(PYTHON) scripts/version.py

release-test:
	$(PYTHON) -m unittest discover -s scripts -p 'test_*.py' -v

smoke: build
	$(PYTHON) scripts/pty_smoke.py

package:
	GO="$(GO)" $(PYTHON) scripts/package.py

release:
	$(PYTHON) scripts/release.py "$(PART)" $(if $(VERSION),--new-version "$(VERSION)")

dist:
	@mkdir -p dist
	@for os in linux darwin; do \
		for arch in amd64 arm64; do \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags='-s -w' \
				-o dist/termfana-$$os-$$arch ./cmd/termfana || exit 1; \
		done; \
	done
