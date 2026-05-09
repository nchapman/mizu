.PHONY: lint lint-go lint-ts fmt test build check

# Run every check the CI would run. Use this before committing.
check: lint test build

lint: lint-go lint-ts

lint-go:
	@out=$$(gofmt -l . 2>&1); if [ -n "$$out" ]; then echo "gofmt needs to run on:"; echo "$$out"; exit 1; fi
	go vet ./...
	staticcheck ./...

lint-ts:
	cd admin && npm run lint && npm run typecheck

fmt:
	gofmt -w .
	cd admin && npm run lint:fix

test:
	go test ./...

build:
	go build ./...
	cd admin && npm run build
