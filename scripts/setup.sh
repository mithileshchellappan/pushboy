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
  release="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest")" || return
  printf '%s\n' "$release" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

if [ -z "$VERSION" ]; then
  require_cmd curl
  if ! VERSION="$(latest_release)" || [ -z "$VERSION" ]; then
    echo "error: could not resolve the latest release. Set PUSHBOY_VERSION to a release tag or main." >&2
    exit 1
  fi
fi

if [ "$VERSION" = "main" ]; then
  SOURCE_REF=refs/heads/main
elif printf '%s\n' "$VERSION" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$'; then
  SOURCE_REF="refs/tags/$VERSION"
else
  echo "error: expected a release tag or main, got $VERSION" >&2
  exit 1
fi

echo "Installing Pushboy ${VERSION} into ${INSTALL_DIR}"

if [ -e "$INSTALL_DIR" ] && [ ! -e "$INSTALL_DIR/.git" ] &&
   { [ ! -d "$INSTALL_DIR" ] || [ "$(ls -A "$INSTALL_DIR")" ]; }; then
  echo "error: ${INSTALL_DIR} already exists and is not an empty directory" >&2
  echo "Set PUSHBOY_HOME to a different directory." >&2
  exit 1
fi

if [ -e "$INSTALL_DIR/.git" ] || command -v git >/dev/null 2>&1; then
  require_cmd git
  if [ ! -e "$INSTALL_DIR/.git" ]; then
    mkdir -p "$INSTALL_DIR"
    git init --quiet "$INSTALL_DIR"
    git -C "$INSTALL_DIR" remote add origin "$REPO_URL"
  fi
  git -C "$INSTALL_DIR" fetch --quiet --depth 1 --no-tags origin "$SOURCE_REF"
  git -C "$INSTALL_DIR" checkout --quiet --detach FETCH_HEAD
else
  require_cmd curl
  require_cmd tar
  archive="$(mktemp)"
  trap 'rm -f "$archive"' EXIT
  curl -fsSL "https://github.com/${REPO_SLUG}/archive/${SOURCE_REF}.tar.gz" -o "$archive"
  mkdir -p "$INSTALL_DIR"
  tar -xzf "$archive" --strip-components=1 -C "$INSTALL_DIR"
fi

mkdir -p "$INSTALL_DIR/keys"

settings="$INSTALL_DIR/.env"
source_settings="$settings"
if [ ! -f "$settings" ] && [ -f "$INSTALL_DIR/.env.example" ]; then
  source_settings="$INSTALL_DIR/.env.example"
fi
updated_settings="$(mktemp "$INSTALL_DIR/.env.XXXXXX")"
trap 'rm -f "$updated_settings" ${archive:+"$archive"}' EXIT
if [ -f "$source_settings" ]; then
  awk '!/^[[:space:]]*(export[[:space:]]+)?PUSHBOY_(IMAGE|COMPOSE_SERVICE)[[:space:]]*=/' \
    "$source_settings" > "$updated_settings"
fi
compose_command="docker compose up --build"
if [ "$VERSION" != main ] && [ "$VERSION" != v0.0.0 ]; then
  image="ghcr.io/$(printf '%s' "$REPO_SLUG" | tr '[:upper:]' '[:lower:]'):$VERSION"
  printf '\nPUSHBOY_IMAGE=%s\nPUSHBOY_COMPOSE_SERVICE=release\n' "$image" >> "$updated_settings"
  compose_command="docker compose up --no-build"
fi
mv -f "$updated_settings" "$settings"

cat <<EOF

Pushboy is ready at:
  ${INSTALL_DIR}

Next steps:
  cd "${INSTALL_DIR}"
  edit .env
  ${compose_command}

Health check:
  curl http://localhost:8080/v1/ping

Provider credentials:
  - APNS .p8 files go in ${INSTALL_DIR}/keys
  - Firebase service-account JSON goes in ${INSTALL_DIR}/keys/service-account.json

EOF
