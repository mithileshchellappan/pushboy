#!/usr/bin/env bash
set -euo pipefail

revision="$(git rev-parse HEAD)"
repo="repos/$GITHUB_REPOSITORY"
package="$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"
VERSION="${VERSION:?Release tag required}"
image="ghcr.io/$package:$VERSION"
files=(dist/SHA256SUMS dist/pushboy_"${VERSION}"_{linux,darwin}_{amd64,arm64}.tar.gz)
prerelease=false
latest=legacy
if [[ "$VERSION" == *-rc.* ]]; then
    prerelease=true
    latest=false
fi

verify_tag() {
    local ref="refs/tags/$VERSION" remote_revision
    remote_revision="$(git ls-remote origin "$ref" "$ref^{}" |
        awk '/\^\{\}$/ { peeled=$1 } !/\^\{\}$/ { tag=$1 } END { print (peeled ? peeled : tag) }')"
    if [[ "$remote_revision" != "$revision" ]]; then
        echo "Remote tag $VERSION no longer points to $revision" >&2
        exit 1
    fi
}

release_for_tag() {
    gh api --paginate --slurp "$repo/releases" |
        jq -c --arg tag "$VERSION" '[.[][] | select(.tag_name == $tag)] |
            if length > 1 then error("Multiple releases for tag") else .[0] // {} end'
}

version_image_digest() {
    local result
    if result="$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$image" 2>&1)"; then
        [[ "$result" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "Invalid image digest for $image" >&2; return 1; }
        printf '%s\n' "$result"
    elif [[ "$result" != *"$image: not found"* && "$result" != *"manifest unknown"* ]]; then
        printf '%s\n' "$result" >&2
        return 1
    fi
}

verify_tag
release="$(release_for_tag)"
marker="Built from commit \`$revision\`."
if [[ "$release" == '{}' ]]; then
    notes="$marker"
    if [[ "$VERSION" == v0.* ]]; then
        notes+=$'\n\nPushboy 0.x is a preview. Breaking changes can ship between releases.'
    fi
    changes="$(awk -v version="$VERSION" '/^## / { if (found) exit; found = ($2 == version); next } found' CHANGELOG.md)"
    if [[ -n "$changes" ]]; then
        notes+=$'\n\n'"$changes"
    fi
    created_url="$(gh release create "$VERSION" --verify-tag --draft --target "$revision" \
        --prerelease="$prerelease" --title "$VERSION" --generate-notes --notes "$notes")"
    printf '%s\n' "$created_url"
    for delay in 0 1 2 4 8; do
        if [[ "$delay" != 0 ]]; then
            sleep "$delay"
        fi
        release="$(release_for_tag)"
        [[ "$release" == '{}' ]] || break
    done
    if [[ "$release" == '{}' ]]; then
        echo "Created draft $VERSION is not yet visible in the release list. Rerun this job." >&2
        exit 1
    fi
    if ! jq -e --arg url "$created_url" '$url != "" and .draft == true and .html_url == $url' <<< "$release" > /dev/null; then
        echo "Release $VERSION does not match the newly created draft" >&2
        exit 1
    fi
    verify_tag
fi

if ! release_id="$(jq -er --arg marker "$marker" --argjson prerelease "$prerelease" '
    select(.prerelease == $prerelease and ((.body // "") | split("\n")[0] == $marker)) | .id' <<< "$release")"; then
    echo "Existing release $VERSION does not match this build" >&2
    exit 1
fi
draft="$(jq -r '.draft' <<< "$release")"
assets="$repo/releases/$release_id/assets"
if [[ "$draft" == true ]]; then
    for file in "${files[@]}"; do
        name="${file##*/}"
        asset="$(jq -c --arg name "$name" '.assets[] | select(.name == $name)' <<< "$release")"
        state="$(jq -r '.state' <<< "$asset")"
        if [[ "$state" == starter ]]; then
            gh api --method DELETE "$repo/releases/assets/$(jq -er '.id' <<< "$asset")"
            state=""
        fi
        if [[ -z "$state" ]]; then
            gh release upload "$VERSION" "$file"
        fi
    done
fi

uploaded="$(gh api --paginate --slurp "$assets")"
jq -e --argjson count "${#files[@]}" '[.[][]] | length == $count' <<< "$uploaded" > /dev/null ||
    { echo "Release has an unexpected number of assets" >&2; exit 1; }
for file in "${files[@]}"; do
    digest="$(sha256sum "$file" | cut -d ' ' -f 1)"
    jq -e --arg name "${file##*/}" --arg digest "sha256:$digest" '
        any(.[][]; .name == $name and .state == "uploaded" and .digest == $digest)
    ' <<< "$uploaded" > /dev/null || { echo "Release asset verification failed: $file" >&2; exit 1; }
done

if [[ "$draft" == false ]]; then
    echo "GitHub Release $VERSION already contains the verified assets"
    exit 0
fi

verify_tag
printf '%s' "$GH_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
digest="$(version_image_digest)"
if [[ -z "$digest" ]]; then
    stage="$(mktemp -d)"
    trap 'rm -rf "$stage"' EXIT
    source="org.opencontainers.image.source=$GITHUB_SERVER_URL/$GITHUB_REPOSITORY"
    version="org.opencontainers.image.version=$VERSION"
    rev="org.opencontainers.image.revision=$revision"
    docker buildx build --platform linux/amd64,linux/arm64 --metadata-file "$stage/image.json" \
        --output "type=image,name=${image%:*},push=true,push-by-digest=true,name-canonical=true,oci-mediatypes=true" \
        --build-arg "VERSION=$VERSION" --build-arg "REVISION=$revision" \
        --label "$source" --label "$version" --label "$rev" \
        --annotation "index:$source" --annotation "index:$version" --annotation "index:$rev" .
    digest="$(jq -er '."containerimage.digest" | select(test("^sha256:[0-9a-f]{64}$"))' "$stage/image.json")"
fi

pinned="${image%:*}@$digest"
manifest="$(docker buildx imagetools inspect --raw "$pinned")"
jq -e --arg version "$VERSION" --arg revision "$revision" --arg source "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY" '
    .annotations["org.opencontainers.image.version"] == $version and
    .annotations["org.opencontainers.image.revision"] == $revision and
    .annotations["org.opencontainers.image.source"] == $source and
    ([.manifests[].platform | select(.os == "linux") | .architecture] | sort) == ["amd64", "arm64"]
' <<< "$manifest" > /dev/null
for arch in amd64 arm64; do
    test "$(docker run --rm --pull=always --platform "linux/$arch" "$pinned" --version)" = "pushboy $VERSION ($revision)"
done

verify_tag
current_digest="$(version_image_digest)"
if [[ -z "$current_digest" ]]; then
    docker buildx imagetools create --tag "$image" "$pinned"
elif [[ "$current_digest" != "$digest" ]]; then
    echo "$image already points to another digest" >&2
    exit 1
fi

if ! token="$(curl -fsS --get --data-urlencode "scope=repository:$package:pull" \
    'https://ghcr.io/token?service=ghcr.io' | jq -er .token)" ||
   ! headers="$(curl -fsSI -H "Authorization: Bearer $token" \
    -H 'Accept: application/vnd.oci.image.index.v1+json' "https://ghcr.io/v2/$package/manifests/$VERSION")"; then
    echo "$image is not publicly pullable. Set the package visibility to Public and rerun the workflow." >&2
    exit 1
fi
public_digest="$(awk 'tolower($1) == "docker-content-digest:" { sub(/\r$/, "", $2); print $2 }' <<< "$headers")"
if [[ "$public_digest" != "$digest" ]]; then
    echo "$image does not resolve to the verified digest" >&2
    exit 1
fi

verify_tag
notes="$(jq -er .body <<< "$release")"
notes+=$'\n\n'"Container image: \`$pinned\`."
jq -n --arg latest "$latest" --arg body "$notes" '{draft: false, make_latest: $latest, body: $body}' |
    gh api --method PATCH "$repo/releases/$release_id" --input - --jq .html_url
