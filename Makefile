.PHONY: frontend build build-debug run debug run-debug test

ARGS ?=

frontend:
	cd frontend && npm ci
	cd frontend && npm run build

build: frontend
	mkdir -p bin
	cd backend && go build -o ../bin/anyviewer ./cmd/anyviewer

build-debug: frontend
	mkdir -p bin
	cd backend && go build -gcflags="all=-N -l" -o ../bin/anyviewer-debug ./cmd/anyviewer

run: frontend
	cd backend && go run ./cmd/anyviewer $(ARGS)

debug: frontend
	cd backend && go run -gcflags="all=-N -l" ./cmd/anyviewer $(ARGS)

run-debug: debug

test: frontend
	cd backend && go test -race ./...
