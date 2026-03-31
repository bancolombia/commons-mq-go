.PHONY: test test-unit test-integration lint build

GO ?= go
GOFLAGS ?=
MODULE := github.com/bancolombia/commons-mq-go

build:
	$(GO) build $(GOFLAGS) ./...

test: test-unit test-integration

test-unit:
	$(GO) test $(GOFLAGS) ./tests/unit/... -v -count=1

test-integration:
	$(GO) test $(GOFLAGS) -tags integration ./tests/integration/... -v -count=1 -timeout 120s

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy
