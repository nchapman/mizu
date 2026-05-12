.PHONY: lint lint-go lint-ts lint-cloud-init fmt test build check

# Run every check the CI would run. Use this before committing.
check: lint test build

lint: lint-go lint-ts lint-cloud-init

# Cloud-init user-data is pasted into provider control panels that
# routinely re-encode non-ASCII bytes. The resulting #x0080 etc.
# trip cloud-init's YAML parser, which silently discards the whole
# config as "empty cloud config" -- nothing runs on first boot.
# Keep deploy/cloud-init.yaml ASCII-only.
lint-cloud-init:
	@bad=$$(perl -ne 'print "$$.: $$_" if /[^\x00-\x7F]/' deploy/cloud-init.yaml); \
	if [ -n "$$bad" ]; then \
		echo "deploy/cloud-init.yaml must be ASCII-only:"; \
		echo "$$bad"; \
		exit 1; \
	fi

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
	cd admin && npm test

build:
	go build ./...
	cd admin && npm run build
