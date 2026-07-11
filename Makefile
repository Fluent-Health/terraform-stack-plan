.PHONY: demo test

test:
	go test -v ./...

demo:
	go run ./cmd/tfstackplan serve --demo --addr :8080

