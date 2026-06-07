GO = /usr/local/go/bin/go
TAGS = goolm

.PHONY: build vet test test-integration lint fmt

build:
	$(GO) build -tags $(TAGS) ./...

vet:
	$(GO) vet -tags $(TAGS) ./...

test:
	$(GO) test -tags $(TAGS) -race -count=1 ./...

test-integration:
	$(GO) test -tags "$(TAGS) integration" -race -run Integration ./channel/matrix/...

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest -tags $(TAGS) ./...

fmt:
	$(GO) fmt ./...
