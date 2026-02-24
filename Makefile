.PHONY: build compile deploy deps dev clean

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

# Install dependencies
deps:
	go mod tidy

deploy:
	@# inject the variables required to deploy (but don't track them in git)
	@if [ ! -f __config.sh ]; then \
		echo ""; \
		echo "Error: __config.sh not found. Copy __config.sh.example to __config.sh and configure it."; \
		echo ""; \
		exit 1; \
	fi
	@source __config.sh; \
	if [[ -z $${USER} || -z $${GROUP} || -z $${SERVER} ]]; then \
		echo ""; \
		echo "Config values required for 'USER', 'GROUP' and 'SERVER'"; \
		echo ""; \
		exit 1; \
	fi; \
	echo ""; \
	echo "deploying jiratime..."; \
	ssh $${USER}@$${SERVER} "mkdir -p /home/$${USER}/jiratime"; \
	echo ""; \
	echo "copying binary..."; \
	scp ./bin/jiratime-linux-amd64 $${USER}@$${SERVER}:/home/$${USER}/jiratime; \
	echo ""; \
	echo "copying supervisor config..."; \
	scp ./supervisor.conf $${USER}@$${SERVER}:/home/$${USER}/jiratime; \
	echo ""; \
	echo "copying config..."; \
	scp ./config.prod.toml $${USER}@$${SERVER}:/home/$${USER}/jiratime; \
	echo ""; \
	echo "installing update..."; \
	ssh $${USER}@$${SERVER} "\
		mv /home/$${USER}/jiratime/jiratime-linux-amd64 /home/$${USER}/jiratime/jiratime && \
		sudo chown $${USER}:$${GROUP} /home/$${USER}/jiratime/jiratime && \
		mv /home/$${USER}/jiratime/config.prod.toml /home/$${USER}/jiratime/config.toml && \
		sed -i 's|/home/cca-user/jiratime|/home/$${USER}/jiratime|g' /home/$${USER}/jiratime/supervisor.conf && \
		sudo mv /home/$${USER}/jiratime/supervisor.conf /etc/supervisor/conf.d/jiratime.conf && \
		sudo supervisorctl restart jiratime"; \
	echo ""; \
	echo "service restarted..."