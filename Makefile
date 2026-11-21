VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/master2sid2/cert-alerter/internal/version.Version=$(VERSION)
MYSQL_TEST_DSN ?= root:rootpw@tcp(127.0.0.1:13306)/

# Installation (make install). DESTDIR stages the files for packaging; the
# service user is created and systemd reloaded only when DESTDIR is empty.
PREFIX       ?= /usr/local
BINDIR       ?= $(PREFIX)/bin
SYSCONFDIR   ?= /etc
UNITDIR      ?= /etc/systemd/system
SERVICE_USER ?= cert-alerter
DESTDIR      ?=
DEFAULTS     := $(SYSCONFDIR)/default/cert-alerter

.PHONY: build build-darwin-arm64 run dev test test-mysql mysql-up mysql-down vet fmt docker install uninstall clean

build: ## static binary in ./bin (no CGO)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cert-alerter ./cmd/cert-alerter

# The project is pure Go (SQLite via modernc.org/sqlite), so it cross-compiles
# from any OS without a C toolchain.
build-darwin-arm64: ## macOS on Apple Silicon (M1/M2/M3…) -> bin/cert-alerter-darwin-arm64
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cert-alerter-darwin-arm64 ./cmd/cert-alerter

run: build ## run with SQLite in ./data
	./bin/cert-alerter serve

dev: ## serve the web UI from disk: UI edits need no rebuild
	go run ./cmd/cert-alerter serve -static-dir web/static -log-level debug

test: ## unit and integration tests (SQLite)
	go test ./...

mysql-up: ## throw-away MySQL for tests
	docker run -d --name cert-alerter-test-mysql -e MYSQL_ROOT_PASSWORD=rootpw -p 127.0.0.1:13306:3306 mysql:8.4
	@echo "waiting for MySQL..."; until docker exec cert-alerter-test-mysql mysql -uroot -prootpw -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done; sleep 5

mysql-down:
	docker rm -f cert-alerter-test-mysql

test-mysql: ## all tests against SQLite and MySQL (start it with `make mysql-up`)
	CERT_ALERTER_TEST_MYSQL_DSN='$(MYSQL_TEST_DSN)' go test -count=1 -timeout 20m ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal web

docker: ## container image
	docker build --build-arg VERSION=$(VERSION) -t cert-alerter:$(VERSION) -t cert-alerter:latest .

# Build as a regular user first, then install as root:
#   make build && sudo make install
install: ## install the binary, the systemd unit and /etc/default/cert-alerter; creates the service user
	@test -x bin/cert-alerter || { echo "bin/cert-alerter not found: run 'make build' first (as a regular user)"; exit 1; }
	install -d -m 0755 $(DESTDIR)$(BINDIR) $(DESTDIR)$(UNITDIR) $(DESTDIR)$(SYSCONFDIR)/default
	install -m 0755 bin/cert-alerter $(DESTDIR)$(BINDIR)/cert-alerter
	sed -e 's|/usr/local/bin/cert-alerter|$(BINDIR)/cert-alerter|' \
	    -e 's|/etc/default/cert-alerter|$(DEFAULTS)|' \
	    -e 's|^User=cert-alerter$$|User=$(SERVICE_USER)|' -e 's|^Group=cert-alerter$$|Group=$(SERVICE_USER)|' \
	    deploy/systemd/cert-alerter.service > $(DESTDIR)$(UNITDIR)/cert-alerter.service
	chmod 0644 $(DESTDIR)$(UNITDIR)/cert-alerter.service
	@if [ -e $(DESTDIR)$(DEFAULTS) ]; then \
	    install -m 0640 deploy/systemd/cert-alerter.default $(DESTDIR)$(DEFAULTS).new; \
	    echo "kept the existing $(DEFAULTS); the current example is in $(DEFAULTS).new"; \
	else \
	    install -m 0640 deploy/systemd/cert-alerter.default $(DESTDIR)$(DEFAULTS); \
	fi
	@if [ -z "$(DESTDIR)" ]; then \
	    if ! getent passwd $(SERVICE_USER) >/dev/null; then \
	        useradd --system --user-group --home-dir /var/lib/cert-alerter --no-create-home \
	            --shell /usr/sbin/nologin --comment "Cert Alerter" $(SERVICE_USER) && \
	        echo "created system user $(SERVICE_USER)"; \
	    fi; \
	    chown root:$(SERVICE_USER) $(DEFAULTS); \
	    systemctl daemon-reload; \
	    echo; \
	    echo "Installed. Next steps:"; \
	    echo "  1. edit $(DEFAULTS)"; \
	    echo "  2. systemctl enable --now cert-alerter"; \
	    echo "  3. journalctl -u cert-alerter | grep setup   # first-run setup link"; \
	fi

uninstall: ## remove the binary and the systemd unit; keeps the config, the data and the user
	@if [ -z "$(DESTDIR)" ] && systemctl list-unit-files cert-alerter.service >/dev/null 2>&1; then \
	    systemctl disable --now cert-alerter.service || true; \
	fi
	rm -f $(DESTDIR)$(BINDIR)/cert-alerter $(DESTDIR)$(UNITDIR)/cert-alerter.service
	@if [ -z "$(DESTDIR)" ]; then systemctl daemon-reload; fi
	@echo "kept $(DEFAULTS), /var/lib/cert-alerter, /var/log/cert-alerter and the user $(SERVICE_USER)"

clean:
	go clean
	test ! -d bin || rm -r bin

# Maintainer targets, not part of the published sources.
-include release.mk
