APP=comstac

.PHONY: run build test test-api test-smtp test-store fmt install uninstall

run:
	go run ./cmd/comstac

build:
	go build -trimpath -buildvcs=true -o $(APP) ./cmd/comstac

test:
	go test ./...

test-api:
	go test ./internal/api -v

test-smtp:
	go test ./internal/smtpserver -v

test-store:
	go test ./internal/store -v

fmt:
	gofmt -w ./cmd ./internal

install:
	@echo "Refusing in-place install: build and deploy a new immutable versioned release under guarded rollback."
	@false

uninstall:
	@echo "Refusing broad automated uninstall: retire exposure, service, credentials, state and monitoring through a reviewed recovery-aware plan."
	@false
