fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

ci:
	files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "These files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi
	go vet ./...
	go test ./...
	docker build -t ynabup:local .

run:
	go run .

serve:
	go run . -serve

docker-build:
	docker build -t ynabup:local .
