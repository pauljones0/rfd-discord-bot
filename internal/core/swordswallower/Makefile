.PHONY: build-listener check-host create-avd start-avd install-apps enable-listener configure-listener receiver receiver-up receiver-down create-avd-docker start-avd-docker install-apps-docker configure-listener-docker enable-listener-docker phone-forward install-udev-forward

build-listener:
	./scripts/build-listener.sh

phone-forward:
	./scripts/phone-forward.sh

install-udev-forward:
	./scripts/install-udev-forward.sh

check-host:
	./scripts/check-host.sh

create-avd:
	./scripts/create-avd.sh

start-avd:
	./scripts/start-avd.sh

install-apps:
	./scripts/install-apps.sh

enable-listener:
	./scripts/enable-listener.sh

configure-listener:
	./scripts/configure-listener.sh

create-avd-docker:
	./scripts/create-avd-docker.sh

start-avd-docker:
	./scripts/start-avd-docker.sh

install-apps-docker:
	ADB=./scripts/adb-docker.sh ./scripts/install-apps.sh

configure-listener-docker:
	ADB=./scripts/adb-docker.sh ./scripts/configure-listener.sh

enable-listener-docker:
	ADB=./scripts/adb-docker.sh ./scripts/enable-listener.sh

receiver:
	python3 tools/receiver.py

receiver-up:
	docker compose up -d --build notification-receiver

receiver-down:
	docker compose down
