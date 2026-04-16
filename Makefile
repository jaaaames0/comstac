APP=comstac
INSTALL_BIN=/usr/local/bin/comstac
SERVICE_ENV=/etc/comstac/comstac.env
SERVICE_FILE=/etc/systemd/system/comstac.service
BACKUP_SERVICE=/etc/systemd/system/comstac-backup.service
BACKUP_TIMER=/etc/systemd/system/comstac-backup.timer
DATA_DIR=/var/lib/comstac

.PHONY: run test test-api test-smtp test-store fmt install install-timer uninstall backup

run:
	go run ./cmd/comstac

test:
	go test ./...

test-api:
	go test ./internal/api -v

test-smtp:
	go test ./internal/smtpserver -v

test-store:
	go test ./internal/store -v

fmt:
	gofmt -w ./cmd ./internal

install:
	go build -o $(INSTALL_BIN) ./cmd/comstac
	mkdir -p $(DATA_DIR)
	mkdir -p /etc/comstac
	cp comstac.env $(SERVICE_ENV)
	chmod 600 $(SERVICE_ENV)
	cp comstac.service $(SERVICE_FILE)
	systemctl daemon-reload
	systemctl enable comstac
	systemctl restart comstac
	@echo "Comstac installed and started. Check status with: systemctl status comstac"

install-timer:
	cp comstac-backup.service $(BACKUP_SERVICE)
	cp comstac-backup.timer $(BACKUP_TIMER)
	systemctl daemon-reload
	systemctl enable --now comstac-backup.timer
	@echo "Backup timer installed. Next run: systemctl list-timers comstac-backup.timer"

backup:
	$(INSTALL_BIN) backup

uninstall:
	systemctl stop comstac || true
	systemctl disable comstac || true
	systemctl disable comstac-backup.timer || true
	rm -f $(SERVICE_FILE) $(BACKUP_SERVICE) $(BACKUP_TIMER) $(INSTALL_BIN)
	systemctl daemon-reload
	@echo "Comstac removed. Data at $(DATA_DIR) and env file preserved."
