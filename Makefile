BINARY := wa
GOBIN  := $(shell go env GOPATH)/bin

.PHONY: build test lint fmt tidy clean

build:
	go build -o $(BINARY) ./cmd/wa

test:
	go test -race ./...

lint:
	go vet ./...
	$(GOBIN)/errcheck ./...
	$(GOBIN)/golangci-lint run ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
