.PHONY: demo screenshots test

test:
	go test -v ./...

demo:
	go run ./cmd/tfstackplan serve --demo --addr :8080

screenshots:
	go test -tags=screenshots -v ./e2e -run TestCaptureScreenshots
