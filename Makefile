APP=comstac

.PHONY: run build test test-api test-smtp test-store test-notifications fmt install uninstall

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

test-notifications:
	node --test internal/ui/testdata/sw.test.cjs

fmt:
	gofmt -w ./cmd ./internal

install:
	@echo "Refusing in-place install: build and deploy a new immutable versioned release under guarded rollback."
	@false

uninstall:
	@echo "Refusing broad automated uninstall: retire exposure, service, credentials, state and monitoring through a reviewed recovery-aware plan."
	@false
