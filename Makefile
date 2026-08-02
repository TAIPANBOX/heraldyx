VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
STATICCHECK ?= staticcheck

.PHONY: build test test-race vet fmt lint staticcheck gates demo clean

build:
	go build $(LDFLAGS) -o bin/heraldyx ./cmd/heraldyx

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet staticcheck
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

staticcheck:
	@command -v $(STATICCHECK) >/dev/null 2>&1 && $(STATICCHECK) ./... || echo "staticcheck not installed; skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"

# Everything CI runs, in the order it runs it.
gates: lint test-race
	./scripts/one-way-out.sh

# One event in, one message out, no mail server involved.
demo: build
	@rm -rf /tmp/heraldyx-demo && mkdir -p /tmp/heraldyx-demo
	@printf '%s\n' '{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T14:00:00Z","source":"tokenfuse","type":"budget_threshold","agent_id":"agent://acme/biller","run_id":"run-42","severity":"medium","data":{"org":"acme","budget_micros":2000000,"spent_micros":1600000}}' > /tmp/heraldyx-demo/events.ndjson
	@HERALDYX_EVENTS=/tmp/heraldyx-demo/events.ndjson \
	 HERALDYX_TO=you@example.com \
	 HERALDYX_MAIL_FILE=/tmp/heraldyx-demo/mail.txt \
	 HERALDYX_CONSOLE_URL=https://box.example.com \
	 HERALDYX_BOX=demo-box \
	 HERALDYX_MIN_SEVERITY=medium \
	 HERALDYX_STATE=/tmp/heraldyx-demo/state.json \
	 ./bin/heraldyx --once --from-now=false
	@echo "----- what would have been sent -----"
	@cat /tmp/heraldyx-demo/mail.txt

clean:
	rm -rf bin
