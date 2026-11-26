#!/usr/bin/env bash
set -euo pipefail

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir "$fixture/bin"
for tool in bash awk jq tr; do
    ln -s "$(command -v "$tool")" "$fixture/bin/$tool"
done

cat > "$fixture/bin/stub" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

revision=0123456789abcdef0123456789abcdef01234567
url=https://github.com/fixture/pushboy/releases/tag/untagged-test-draft
marker="Built from commit \`$revision\`."
case "${0##*/}:$*" in
    git:rev-parse\ HEAD)
        printf '%s\n' "$revision"
        ;;
    git:ls-remote\ origin\ *)
        if [[ "$scenario" == moved-tag && -s "$calls.create" ]]; then
            revision=abcdef0123456789abcdef0123456789abcdef01
        fi
        printf '%s\trefs/tags/%s\n' "$revision" "$VERSION"
        ;;
    gh:api\ --paginate\ --slurp\ repos/fixture/pushboy/releases)
        if [[ "$scenario" == mismatching ]]; then
            marker="Wrong build marker."
        elif [[ "$scenario" == timeout || ! -s "$calls.create" || ! -f "$calls.missed" ]]; then
            if [[ -s "$calls.create" ]]; then
                : > "$calls.missed"
            fi
            printf '[[]]\n'
            exit 0
        fi
        jq -nc --arg url "$url" --arg tag "$VERSION" --arg body "$marker" \
            '[[{id: 1, html_url: $url, tag_name: $tag, body: $body, draft: true, prerelease: false, assets: []}]]'
        ;;
    gh:release\ create\ *)
        printf 'create\n' >> "$calls.create"
        if [[ "$scenario" == wrong-url ]]; then
            url="$url-other"
        fi
        printf '%s\n' "$url"
        ;;
    gh:release\ upload\ *)
        printf 'upload\n' >> "$calls.upload"
        exit 73
        ;;
    sleep:*)
        :
        ;;
    *)
        printf 'Unexpected fixture command: %s %s\n' "$0" "$*" >&2
        exit 99
        ;;
esac
STUB
chmod +x "$fixture/bin/stub"
for tool in git gh sleep; do
    ln -s stub "$fixture/bin/$tool"
done

check_scenario() {
    local scenario="$1" expected_status="$2" creates="$3" uploads="$4" message="${5:-}"
    local calls="$fixture/$scenario" status=0
    : > "$calls.create"
    : > "$calls.upload"
    PATH="$fixture/bin" GITHUB_REPOSITORY=fixture/pushboy VERSION=v1.2.3 \
        scenario="$scenario" calls="$calls" bash scripts/publish-release.sh > "$fixture/output" 2>&1 || status=$?
    if [[ "$status" != "$expected_status" ||
        "$(wc -l < "$calls.create")" -ne "$creates" || "$(wc -l < "$calls.upload")" -ne "$uploads" ]] ||
        { [[ -n "$message" ]] && ! grep -Fq "$message" "$fixture/output"; }; then
        printf 'Unexpected release result for %s (exit %s)\n' "$scenario" "$status" >&2
        cat "$fixture/output" "$calls.create" "$calls.upload" >&2
        exit 1
    fi
}

check_scenario delayed 73 1 1
check_scenario timeout 1 1 0 'is not yet visible in the release list'
check_scenario mismatching 1 0 0 'does not match this build'
check_scenario wrong-url 1 1 0 'does not match the newly created draft'
check_scenario moved-tag 1 1 0 'Remote tag v1.2.3 no longer points to'
