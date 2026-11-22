.PHONY: fmt vet test test-race test-container ci run serve docker-build

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-container:
	go test -tags=container_smoke -count=1 -v -timeout=8m ./tests/container

ci:
	files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "These files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi
	go vet ./...
	go test ./...
	go test -race ./...
	$(MAKE) test-container

run:
	go run .

serve:
	go run . -serve

docker-build:
	docker build -t ynabup:local .
