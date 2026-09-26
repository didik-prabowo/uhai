# Every recipe here is a one-liner. The Makefile exists so that CGO_ENABLED=0
# is not something to remember: without it the linker fails to build the test
# binaries in a sandbox, and says nothing useful about why.

GO ?= go
BINARY := uhai
ENV := CGO_ENABLED=0
SOURCES := $(shell git ls-files '*.go')

.PHONY: build install run test race vet windows fmt check clean release-snapshot demo

build: ## the binary, here
	$(ENV) $(GO) build -o $(BINARY) ./cmd/uhai

install: ## the binary, onto your PATH
	$(ENV) $(GO) install ./cmd/uhai
	@uhai -daemon-stop

run: build ## build it and open it
	./$(BINARY)

test:
	$(ENV) $(GO) test ./...

# The one target that must not have CGO_ENABLED=0 in front of it: the race
# detector is built out of cgo and refuses outright without it. It therefore
# cannot run in the sandbox the rest of this file exists for — it is here for
# CI, and for anyone whose linker will build a cgo test binary.
race: ## the tests again, watching for data races (needs cgo)
	CGO_ENABLED=1 $(GO) test -race ./...

vet:
	$(ENV) $(GO) vet ./...

windows: ## check the build nobody here can run
	GOOS=windows GOARCH=amd64 $(ENV) $(GO) vet ./...

fmt: ## format in place
	gofmt -w $(SOURCES)

check: vet windows test ## what /check runs: formatting, vet, the Windows build, tests
	@unformatted=$$(gofmt -l $(SOURCES)); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi

# A release without tagging one, so .goreleaser.yaml is proved before a tag
# has to be deleted to fix it. Needs goreleaser: `brew install goreleaser`.
release-snapshot: ## build the release artefacts locally, publishing nothing
	goreleaser release --snapshot --clean

# Records the README demo against demo/mock, a scripted endpoint, so it costs
# nothing and names no real provider. Needs VHS: `brew install vhs`.
#
# The GIF is regenerated in place at docs/assets/demo.gif and committed with
# the rest — it is under a megabyte, which is cheaper than asking a reader to
# fetch it from somewhere that may go away. The recording makes one real edit
# to cmd/uhai/main.go; the last line puts it back.
demo: build ## record the README demo against the scripted endpoint
	@mkdir -p local/demo-home/.uhai
	@printf '{"model":"demo/demo-model","baseUrls":{"demo":"http://127.0.0.1:8977/v1"}}\n' > local/demo-home/.uhai/settings.json
	@printf '{"demo":{"key":"not-a-real-key"}}\n' > local/demo-home/.uhai/auth.json
	@chmod 600 local/demo-home/.uhai/auth.json
	@# Built rather than `go run`: go run's pid is a wrapper, and killing it
	@# leaves the server holding the port.
	$(ENV) $(GO) build -o local/demo-mock ./demo/mock
	./local/demo-mock & echo $$! > local/demo-mock.pid
	@sleep 1
	-vhs demo/demo.tape
	@kill `cat local/demo-mock.pid` 2>/dev/null; rm -f local/demo-mock.pid
	git checkout -- cmd/uhai/main.go

clean:
	rm -f $(BINARY)
	rm -rf dist
