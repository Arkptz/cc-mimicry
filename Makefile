.PHONY: build test lint tidy vuln clean

CPA_VERSION ?= $(shell cat .cpa-version 2>/dev/null || echo v7.2.51)
VERSION     ?= 0.1.0
OUT_DIR     ?= dist

build:
	mkdir -p $(OUT_DIR)
	CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
		-ldflags="-s -w -X 'main.pluginVersion=$(VERSION)'" \
		-o $(OUT_DIR)/cc-mimicry.so .
	rm -f $(OUT_DIR)/cc-mimicry.h

test:
	CGO_ENABLED=1 go test -race -count=1 ./...

lint:
	CGO_ENABLED=1 golangci-lint run ./...

tidy:
	go mod tidy

vuln:
	govulncheck ./...

clean:
	rm -rf $(OUT_DIR)
