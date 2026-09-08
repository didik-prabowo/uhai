# Every recipe here is a one-liner. The Makefile exists so that CGO_ENABLED=0
# is not something to remember: without it the linker fails to build the test
# binaries in a sandbox, and says nothing useful about why.

GO ?= go
BINARY := uhai
ENV := CGO_ENABLED=0
SOURCES := $(shell git ls-files '*.go')

.PHONY: build install run test race vet windows fmt check clean

build: ## the binary, here
	$(ENV) $(GO) build -o $(BINARY) ./cmd/uhai

install: ## the binary, onto your PATH
	$(ENV) $(GO) install ./cmd/uhai
	@uhai -daemon-stop

run: build ## build it and open it
	./$(BINARY)

test:
	$(ENV) $(GO) test ./...

race: ## the tests again, watching for data races
	$(ENV) $(GO) test -race ./...

vet:
	$(ENV) $(GO) vet ./...

windows: ## check the build nobody here can run
	GOOS=windows GOARCH=amd64 $(ENV) $(GO) vet ./...

fmt: ## format in place
	gofmt -w $(SOURCES)

check: vet windows test ## what /check runs: formatting, vet, the Windows build, tests
	@unformatted=$$(gofmt -l $(SOURCES)); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi

clean:
	rm -f $(BINARY)
