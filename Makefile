# scorpius build
#
# Static binaries only. CGO is disabled by default.

GO              ?= go
BIN_DIR         ?= bin
CGO_ENABLED     ?= 0
GOFLAGS         ?=
LDFLAGS         ?= -s -w

AGENT_PKG       := ./cmd/scorpius
AGENT_BIN       := $(BIN_DIR)/scorpius

.PHONY: all build agent test test-integration lint vet fmt tidy clean

all: build

build: agent

agent:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(AGENT_BIN) $(AGENT_PKG)

test:
	$(GO) test ./...

test-integration:
	$(GO) test -tags=integration ./...

lint: vet
	@which staticcheck >/dev/null 2>&1 || { \
		echo "staticcheck not installed. Run: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
		exit 1; \
	}
	staticcheck ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
