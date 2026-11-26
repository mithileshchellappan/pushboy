#!/usr/bin/env sh
set -eu

unset COMPOSE_FILE COMPOSE_DISABLE_ENV_FILE COMPOSE_PROJECT_NAME COMPOSE_ENV_FILES
unset PUSHBOY_COMPOSE_SERVICE PUSHBOY_IMAGE
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
origin="$fixture/origin"
install="$fixture/install"
settings="$fixture/shared.env"
git init -q -b main "$origin"
cp compose.yaml compose.pushboy.yaml "$origin"
git -C "$origin" add compose.yaml compose.pushboy.yaml
git -C "$origin" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
git -C "$origin" tag v0.1.0
git clone -q "$origin" "$install"
printf 'APNS_KEY_ID=before\nKEEP_SETTING=original\n' > "$settings"
chmod 640 "$settings"
ln -s ../shared.env "$install/.env"
printf 'services:\n  pushboy:\n    environment:\n      APNS_TEAM_ID: from-local-override\n' > "$install/compose.override.yaml"

setup() {
  PUSHBOY_VERSION="$1" PUSHBOY_HOME="$install" sh scripts/setup.sh > /dev/null
  test "$(readlink "$install/.env")" = ../shared.env
  test -n "$(find "$settings" -perm 0640 -print)"
  grep -qx 'KEEP_SETTING=original' "$settings"
}

config() {
  (cd "$install" && docker compose config --format=json)
}

setup v0.1.0
printf 'FCM_KEY_PATH=keys/later.json\n' >> "$settings"
config | jq -e '.services.pushboy |
  .image == "ghcr.io/mithileshchellappan/pushboy:v0.1.0" and (has("build") | not) and
  .environment.FCM_KEY_PATH == "keys/later.json" and .environment.APNS_TEAM_ID == "from-local-override"'

ln "$settings" "$fixture/unmodified.env"
setup v0.1.0
test "$settings" -ef "$fixture/unmodified.env"

setup main
if grep -q '^PUSHBOY_\(IMAGE\|COMPOSE_SERVICE\)=' "$settings"; then
  exit 1
fi
printf 'APNS_BUNDLE_ID=app.later\n' >> "$settings"
config | jq -e '.services.pushboy |
  has("build") and .environment.APNS_BUNDLE_ID == "app.later" and .environment.APNS_TEAM_ID == "from-local-override"'
