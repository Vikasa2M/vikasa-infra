.PHONY: gen build test integration golden lint staticcheck doc-pdf

# topology model is now hand-written Go (internal/topology); nothing to generate.
gen:
	@echo "no codegen: topology model is hand-written (internal/topology)"

build:
	go build ./...

test:
	go test -count=1 ./...

# Embedded-NATS end-to-end tests (DMZ data path, rendered-config proofs).
# Gated behind the `integration` build tag; not part of the default test run.
integration:
	go test -tags integration -count=1 ./test/integration/

# Regenerate the golden trees (cmd/gen/testdata/golden-*, internal/render/testdata)
# after an intentional output change. ALWAYS review the resulting git diff —
# the diff is the proof the change did only what it claims.
golden:
	UPDATE_GOLDEN=1 go test -count=1 ./cmd/gen/... ./internal/render/...
	git status --short -- cmd/gen/testdata internal/render/testdata

lint:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# staticcheck is not vendored; install once with:
#   go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck:
	staticcheck ./...

doc-pdf:
	pandoc rendered/DEPLOYMENT-GUIDE.md -o rendered/DEPLOYMENT-GUIDE.pdf
