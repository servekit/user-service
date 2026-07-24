.PHONY: all build build-dev test lint generate migrate-up migrate-down fmt vet tidy run proto

# LDFLAGS strips symbol table (-s) and DWARF (-w) and trims local file paths
# (-trimpath) for a smaller, reproducible production binary. Use `make build-dev`
# when you need dlv-debuggable binaries or full panic stack traces.
LDFLAGS := -s -w
GOFLAGS := -trimpath

## build: Build server and migrate binaries into bin/ (production flags)
build:
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o bin/server ./cmd/server/
	go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o bin/migrate ./cmd/migrate/

## build-dev: Build without -s -w -trimpath (for debugging)
build-dev:
	go build -o bin/server ./cmd/server/
	go build -o bin/migrate ./cmd/migrate/

## run: Run the server locally
run:
	go run ./cmd/server/

## test: Run tests with race detector
test:
	go test -race -coverprofile=coverage.out ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt: Format code
fmt:
	gofmt -w .
	goimports -w .

## vet: Run go vet
vet:
	go vet ./...

## generate: Run gorm.io/cli code generation
generate:
	gorm gen -i ./internal/store/models -o ./internal/store/generated

## proto: Generate protobuf code with buf
proto:
	buf generate

## migrate: Run GORM AutoMigrate
migrate:
	go run ./cmd/migrate/

## tidy: Run go mod tidy
tidy:
	go mod tidy

## all: Format, vet, lint, test
all: fmt vet lint test
