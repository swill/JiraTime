.PHONY: build dev clean test vet

# Build the application
build:
	go build -o jiratime .

# Run development server
dev:
	go run .

# Clean build artifacts
clean:
	rm -f jiratime
	rm -f tokens.json

# Run tests
test:
	go test ./...

# Run static analysis
vet:
	go vet ./...

# Install dependencies
deps:
	go mod tidy
