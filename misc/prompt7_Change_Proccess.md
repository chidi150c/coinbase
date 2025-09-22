Generate a full copies of .github/workflows/deploy.yml{{name: Deploy to Linode

on:
  workflow_run:
    workflows: ["Build & Push Images"]
    types: [completed]
    branches: ["main"]

jobs:
  deploy:
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest
    concurrency:
      group: prod-deploy
      cancel-in-progress: false

    steps:
      - name: Prepare SSH key
        env:
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
        run: |
          install -m 700 -d ~/.ssh
          printf "%s" "$SSH_PRIVATE_KEY" > ~/.ssh/id_rsa
          chmod 600 ~/.ssh/id_rsa

      - name: Add host to known_hosts
        env:
          SSH_HOST: ${{ secrets.SSH_HOST }}
        run: |
          ssh-keyscan -H "$SSH_HOST" >> ~/.ssh/known_hosts

      - name: Deploy (git sync + compose rollout)
        env:
          SSH_HOST:  ${{ secrets.SSH_HOST }}
          SSH_USER:  ${{ secrets.SSH_USER }}
          DEPLOY_DIR: ${{ vars.DEPLOY_DIR }}   # e.g. /home/chidi/coinbase/monitoring
          REPO_URL:  https://github.com/${{ github.repository }}.git
        run: |
          ssh -o StrictHostKeyChecking=yes "$SSH_USER@$SSH_HOST" bash -s -- "$DEPLOY_DIR" "$REPO_URL" <<'EOF'
          set -euo pipefail
          DEPLOY_DIR="${1:-/home/chidi/coinbase/monitoring}"
          REPO_URL="$2"
          REPO_DIR="$(dirname "$DEPLOY_DIR")"

          if [ ! -d "$REPO_DIR/.git" ]; then
            mkdir -p "$REPO_DIR"
            git clone "$REPO_URL" "$REPO_DIR"
          else
            git -C "$REPO_DIR" remote set-url origin "$REPO_URL"
            git -C "$REPO_DIR" fetch --all
            git -C "$REPO_DIR" reset --hard origin/main
          fi

          cd "$DEPLOY_DIR"
          docker compose up -d --pull=always --force-recreate
          docker image prune -f
          EOF
}} and .github/workflows/docker.yml{{name: Build & Push Images

on:
  push:
    branches: ["main"]
  workflow_dispatch: {}

env:
  REGISTRY: ghcr.io
  BOT_IMAGE: ghcr.io/${{ github.repository_owner }}/coinbase-bot
  BRIDGE_IMAGE: ghcr.io/${{ github.repository_owner }}/coinbase-bridge

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.repository_owner }}
          password: ${{ secrets.GITHUB_TOKEN }}

      # Bot image
      - name: Build & push bot
        uses: docker/build-push-action@v5
        with:
          context: .
          file: ./Dockerfile
          push: true
          platforms: linux/amd64
          tags: |
            ${{ env.BOT_IMAGE }}:latest
            ${{ env.BOT_IMAGE }}:${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

      # Bridge image
      - name: Build & push bridge
        uses: docker/build-push-action@v5
        with:
          context: ./bridge
          file: ./bridge/Dockerfile
          push: true
          platforms: linux/amd64
          tags: |
            ${{ env.BRIDGE_IMAGE }}:latest
            ${{ env.BRIDGE_IMAGE }}:${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
}} with only the necessary minimal changes to implement {{update CI to build and push ./bridge_binance and ./bridge_hitbtc images and update docker-compose to use those images via image: tags instead of local build: blocks}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{update CI to build and push ./bridge_binance and ./bridge_hitbtc images and update docker-compose to use those images via image: tags instead of local build: blocks}}. Return the complete file, copy-paste ready.