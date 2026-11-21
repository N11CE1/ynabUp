fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

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
	docker build -t ynabup:local .

run:
	go run .

serve:
	go run . -serve

docker-build:
	docker build -t ynabup:local .
