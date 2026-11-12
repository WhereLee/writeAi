.PHONY: run build test clean

run:
	cd backend && go run ./cmd/server

build:
	cd backend && go build -o bin/server ./cmd/server

test:
	cd backend && go test ./...

clean:
	rm -rf backend/bin
