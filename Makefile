.PHONY: dev generate build test

dev:
	go run .

generate:
	go run github.com/a-h/templ/cmd/templ@v0.3.1020 generate

build: generate
	go build -o bin/picg .

test:
	go test ./...
