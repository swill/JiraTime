.PHONY: build dev clean test vet

# Cross compile for different platforms
compile:
	env GOOS=linux GOARCH=amd64 go build -o bin/jiratime-linux-amd64
	env GOOS=darwin GOARCH=amd64 go build -o bin/jiratime-darwin-amd64
	env GOOS=windows GOARCH=amd64 go build -o bin/jiratime-windows-amd64.exe

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
