all: test

deps:
	@go get -d -v ./...
	@go list -f '{{range .TestImports}}{{.}} {{end}}' ./... | xargs -n1 go get -d

build: deps
	@export GOBIN=$(shell pwd)
	@go build

deb: clean build
	@mkdir -p deb/snakk/usr/share/snakk
	@cp -r config.json data ./snakk deb/snakk/usr/share/snakk/
	@dpkg-deb --build deb/snakk

test: deps
	@go test

cover:
	@go test -coverprofile=coverage.out
	@go tool cover -html=coverage.out

clean:
	@go clean
	@rm -f *.out
