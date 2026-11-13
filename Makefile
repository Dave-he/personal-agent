APP := agentd
PKG := ./...

.PHONY: all build run test vet fmt lint tidy docker clean

all: fmt vet test build

build:
	go build -o bin/$(APP) ./cmd/agentd

run:
	go run ./cmd/agentd

test:
	go test -race -count=1 $(PKG)

vet:
	go vet $(PKG)

fmt:
	@gofmt -l . | tee /dev/stderr | (! read)

tidy:
	go mod tidy

docker:
	docker build -t personal-agent:dev .

clean:
	rm -rf bin data
