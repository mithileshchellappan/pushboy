#!/usr/bin/env sh
set -eu

unset COMPOSE_FILE COMPOSE_DISABLE_ENV_FILE COMPOSE_PROJECT_NAME COMPOSE_ENV_FILES
unset PUSHBOY_COMPOSE_SERVICE PUSHBOY_IMAGE
fixture="$(mktemp -d)"
settings_dir="$fixture/shared"
mkdir "$settings_dir"
trap 'chmod u+w "$settings_dir"; rm -rf "$fixture"' EXIT
origin="$fixture/origin"
install="$fixture/install"
settings="$settings_dir/settings.env"
git init -q -b main "$origin"
cp compose.yaml compose.pushboy.yaml "$origin"
git -C "$origin" add compose.yaml compose.pushboy.yaml
git -C "$origin" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
git -C "$origin" tag v0.1.0
git clone -q "$origin" "$install"
printf 'APNS_KEY_ID=before\nKEEP_SETTING=original\n' > "$settings"
chmod 640 "$settings"
ln -s ../shared/settings.env "$install/.env"
printf 'services:\n  pushboy:\n    environment:\n      APNS_TEAM_ID: from-local-override\n' > "$install/compose.override.yaml"

run_setup() {
  PUSHBOY_VERSION="$1" PUSHBOY_HOME="$install" sh scripts/setup.sh
}

setup() {
  run_setup "$1" > /dev/null
  test "$(readlink "$install/.env")" = ../shared/settings.env
  test -n "$(find "$settings" -perm 0640 -print)"
  grep -qx 'KEEP_SETTING=original' "$settings"
}

expect_read_only_failure() {
  if run_setup "$1" > /dev/null 2> "$fixture/error"; then
    echo "installer unexpectedly changed read-only settings" >&2
    exit 1
  fi
  grep -Fq 'settings file or directory is not writable' "$fixture/error"
  test "$settings" -ef "$fixture/unmodified.env"
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
chmod 550 "$settings_dir"
setup v0.1.0
test "$settings" -ef "$fixture/unmodified.env"
expect_read_only_failure main

chmod 750 "$settings_dir"
chmod 440 "$settings"
expect_read_only_failure main
chmod 640 "$settings"
setup main
if grep -q '^PUSHBOY_\(IMAGE\|COMPOSE_SERVICE\)=' "$settings"; then
  exit 1
fi
printf 'APNS_BUNDLE_ID=app.later\n' >> "$settings"
chmod 550 "$settings_dir"
setup main
config | jq -e '.services.pushboy |
  has("build") and .environment.APNS_BUNDLE_ID == "app.later" and .environment.APNS_TEAM_ID == "from-local-override"'
