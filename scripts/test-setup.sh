#!/usr/bin/env sh
set -eu

unset COMPOSE_FILE COMPOSE_DISABLE_ENV_FILE COMPOSE_PROJECT_NAME COMPOSE_ENV_FILES
unset PUSHBOY_COMPOSE_SERVICE PUSHBOY_IMAGE
fixture="$(mktemp -d)"
settings_dir="$fixture/shared"
mkdir "$settings_dir"
origin="$fixture/origin"
install="$fixture/install"
settings="$settings_dir/settings.env"
trap 'chmod u+w "$settings_dir"; if [ -d "$install" ]; then chmod u+w "$install"; fi; rm -rf "$fixture"' EXIT
git init -q -b main "$origin"
cp compose.yaml compose.pushboy.yaml "$origin"
printf 'FIXTURE_VERSION=release\n' > "$origin/.env.example"
git -C "$origin" add compose.yaml compose.pushboy.yaml .env.example
git -C "$origin" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
git -C "$origin" tag v0.1.0
release_revision="$(git -C "$origin" rev-parse HEAD)"
printf 'FIXTURE_VERSION=main\n' > "$origin/.env.example"
printf 'from main\n' > "$origin/main-only"
git -C "$origin" add .env.example main-only
git -C "$origin" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm main
main_revision="$(git -C "$origin" rev-parse HEAD)"
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
  test "$(git -C "$install" rev-parse HEAD)" = "$release_revision"
}

config() {
  (cd "$install" && docker compose config --format=json)
}

setup v0.1.0
test "$(git -C "$install" rev-parse HEAD)" = "$release_revision"
printf 'FCM_KEY_PATH=keys/later.json\n' >> "$settings"
config | jq -e '.services.pushboy |
  .image == "ghcr.io/mithileshchellappan/pushboy:v0.1.0" and (has("build") | not) and
  .environment.FCM_KEY_PATH == "keys/later.json" and .environment.APNS_TEAM_ID == "from-local-override"'

ln "$settings" "$fixture/unmodified.env"
chmod 550 "$install"
setup v0.1.0
test "$settings" -ef "$fixture/unmodified.env"
chmod 750 "$install"

chmod 550 "$settings_dir"
setup v0.1.0
test "$settings" -ef "$fixture/unmodified.env"
expect_read_only_failure main

chmod 750 "$settings_dir"
chmod 440 "$settings"
expect_read_only_failure main
chmod 640 "$settings"

printf 'local change\n' > "$install/main-only"
if run_setup main > /dev/null 2> "$fixture/error"; then
  echo "installer unexpectedly replaced an untracked file" >&2
  exit 1
fi
grep -Fq 'would be overwritten by checkout' "$fixture/error"
test "$(git -C "$install" rev-parse HEAD)" = "$release_revision"
test "$settings" -ef "$fixture/unmodified.env"
rm "$install/main-only"

setup main
test "$(git -C "$install" rev-parse HEAD)" = "$main_revision"
if grep -q '^PUSHBOY_\(IMAGE\|COMPOSE_SERVICE\)=' "$settings"; then
  exit 1
fi
printf 'APNS_BUNDLE_ID=app.later\n' >> "$settings"
chmod 550 "$settings_dir"
setup main
config | jq -e '.services.pushboy |
  has("build") and .environment.APNS_BUNDLE_ID == "app.later" and .environment.APNS_TEAM_ID == "from-local-override"'

PUSHBOY_VERSION=v0.1.0 PUSHBOY_HOME="$fixture/fresh" PUSHBOY_REPO_URL="$origin" sh scripts/setup.sh > /dev/null
grep -qx 'FIXTURE_VERSION=release' "$fixture/fresh/.env"
