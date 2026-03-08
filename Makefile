.PHONY: build logs test-build release deploy

# Run locally
build:
	docker compose up -d --build

logs:
	docker compose logs -f

# Test Docker build (same as GitHub Actions release)
test-build:
	docker build -t twitch-lurker:test .
	@echo "Image built successfully."

# Tag and push release — triggers GitHub Actions to build and push to GHCR
release:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=v1.0.0"; exit 1; fi
	git tag $(VERSION)
	git push origin $(VERSION)
	@echo "Tagged $(VERSION) — GitHub Actions will build and push the image"

# Full deploy flow: review, test, push, release
deploy:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make deploy VERSION=v1.0.0"; exit 1; fi
	git status
	@read -p "Continue? [y/N] " confirm && [ "$$confirm" = y ] || exit 1
	$(MAKE) test-build
	git push
	$(MAKE) release VERSION=$(VERSION)
