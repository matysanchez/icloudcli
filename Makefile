BINARY := icloud-pp-cli
CMD    := ./cmd/$(BINARY)

.PHONY: build vet test clean install

build:
	go build -ldflags="-s -w" -o $(BINARY) $(CMD)

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -f $(BINARY)

install: build
	mv $(BINARY) /usr/local/bin/$(BINARY)

.DEFAULT_GOAL := build
