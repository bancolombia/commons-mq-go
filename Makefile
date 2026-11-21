.PHONY: test test-unit test-integration coverage lint build

GO ?= go
GOFLAGS ?=
MODULE := github.com/bancolombia/commons-mq-go
COVERAGE_EXCLUDES ?= /examples

build:
	$(GO) build $(GOFLAGS) ./...

test: test-unit test-integration

test-unit:
	$(GO) test $(GOFLAGS) ./tests/unit/... -v -count=1

test-integration:
	$(GO) test $(GOFLAGS) -tags integration ./tests/integration/... -v -count=1 -timeout 600s

# Unit-test coverage profile, excluding the examples module(s). -coverpkg is
# needed because the tests live in tests/unit (an external test package); Go
# would otherwise not attribute their execution to the library packages.
coverage:
	pkgs="$$($(GO) list ./... | grep -v '$(COVERAGE_EXCLUDES)')"; \
	$(GO) test $(GOFLAGS) -race -covermode=atomic \
		-coverpkg="$$(echo $$pkgs | tr ' ' ',')" \
		-coverprofile=coverage.out $$pkgs

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy
