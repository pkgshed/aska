golangci := "./tools/golangci-lint/run"

# List available project tasks.
default:
    @just --list

# Run every check that should pass before committing.
check: lint-config fmt-check lint vet test-race verify

# Build the command into bin/ with the version taken from git.
build:
    ./scripts/build.sh

# Distribution packaging stages the installation into a build root, for
# example: just install --prefix=/usr --destdir="$pkgdir"
[doc("Build and install the command into DESTDIR/PREFIX/bin, by default ~/.local/bin.")]
[arg("PREFIX", long="prefix", help="Absolute installation prefix; the command goes into PREFIX/bin")]
[arg("DESTDIR", long="destdir", help="Staging root prepended to PREFIX, for distribution packaging")]
install PREFIX=(home_directory() / ".local") DESTDIR="": build
    ./scripts/install.sh {{ quote(PREFIX) }} {{ quote(DESTDIR) }}

# Run the command from the local checkout.
run *args:
    go run ./cmd/aska {{ args }}

# Run the test suite.
test:
    go test ./...

# Run the test suite with the race detector, bypassing the test cache.
test-race:
    go test -race -count=1 ./...

# Report test coverage per function.
cover:
    mkdir -p bin
    go test -coverprofile=bin/coverage.out ./...
    go tool cover -func=bin/coverage.out

# Run Go's static analysis checks.
vet:
    go vet ./...

# Run correctness and style linters with the pinned golangci-lint.
lint:
    {{ golangci }} run ./...

# Validate the golangci-lint configuration.
lint-config:
    {{ golangci }} config verify

# Format Go files with the configured formatters.
fmt:
    {{ golangci }} fmt ./...

# Check formatting without changing files.
fmt-check:
    {{ golangci }} fmt --diff ./...

# Tidy the module and the pinned tool module.
tidy:
    go mod tidy
    cd tools/golangci-lint && go mod tidy

# Check that both modules are tidy and resolve without changes.
verify:
    go mod tidy -diff
    go list -m -mod=readonly all > /dev/null
    cd tools/golangci-lint && go mod tidy -diff

# Remove build outputs.
clean:
    rm -rf bin
