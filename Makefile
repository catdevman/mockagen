build: clean
	go build -o mockagen cmd/mockagen/main.go

clean:
	rm -rf mockagen

test:
	go test ./...
