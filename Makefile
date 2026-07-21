build: clean
	go build -o mockagen ./cmd/mockagen

clean:
	rm -rf mockagen

test:
	go test ./...
