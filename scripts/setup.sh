#!/usr/bin/env sh
set -eu

REPO_SLUG="${PUSHBOY_REPO:-mithileshchellappan/pushboy}"
REPO_URL="${PUSHBOY_REPO_URL:-https://github.com/${REPO_SLUG}.git}"
INSTALL_DIR="${PUSHBOY_HOME:-$HOME/pushboy}"
VERSION="${PUSHBOY_VERSION:-}"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: missing required command: $1" >&2
    exit 1
  fi
}

latest_release() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" \
      | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
      | head -n 1
  fi
}

if [ -z "$VERSION" ]; then
  VERSION="$(latest_release || true)"
fi

if [ -z "$VERSION" ]; then
  VERSION="main"
fi

echo "Installing Pushboy ${VERSION} into ${INSTALL_DIR}"

if [ -d "$INSTALL_DIR/.git" ]; then
  require_cmd git
  git -C "$INSTALL_DIR" fetch --quiet --tags --depth 1 origin "$VERSION"
  git -C "$INSTALL_DIR" -c advice.detachedHead=false checkout --quiet "$VERSION"
elif [ -e "$INSTALL_DIR" ] && [ "$(find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 | head -n 1)" ]; then
  echo "error: ${INSTALL_DIR} already exists and is not empty" >&2
  echo "Set PUSHBOY_HOME to a different directory or clear the existing path." >&2
  exit 1
elif command -v git >/dev/null 2>&1; then
  mkdir -p "$(dirname "$INSTALL_DIR")"
  git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$VERSION" "$REPO_URL" "$INSTALL_DIR"
else
  require_cmd curl
  require_cmd tar
  mkdir -p "$INSTALL_DIR"
  if [ "$VERSION" = "main" ]; then
    archive_url="https://github.com/${REPO_SLUG}/archive/refs/heads/main.tar.gz"
  else
    archive_url="https://github.com/${REPO_SLUG}/archive/refs/tags/${VERSION}.tar.gz"
  fi
  curl -fsSL "$archive_url" | tar -xz --strip-components=1 -C "$INSTALL_DIR"
fi

mkdir -p "$INSTALL_DIR/keys"

if [ ! -f "$INSTALL_DIR/.env" ] && [ -f "$INSTALL_DIR/.env.example" ]; then
  cp "$INSTALL_DIR/.env.example" "$INSTALL_DIR/.env"
fi

cat <<EOF

Pushboy is ready at:
  ${INSTALL_DIR}

Next steps:
  cd "${INSTALL_DIR}"
  edit .env
  docker compose up --build

Health check:
  curl http://localhost:8080/v1/ping

Provider credentials:
  - APNS .p8 files go in ${INSTALL_DIR}/keys
  - Firebase service-account JSON goes in ${INSTALL_DIR}/keys/service-account.json

EOF
