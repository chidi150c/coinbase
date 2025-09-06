.PHONY: build test docker-bot docker-bridge compose-up

OWNER := chidi150c
REPO  := coinbase

GO := go

build:
	$(GO) build ./...

test:
	$(GO) test -count=1 ./...

docker-bot:
	docker build -f Dockerfile -t ghcr.io/$(OWNER)/$(REPO)-bot:dev .

docker-bridge:
	docker build -f bridge/Dockerfile -t ghcr.io/$(OWNER)/$(REPO)-bridge:dev ./bridge

compose-up:
	cd monitoring && docker compose up -d
