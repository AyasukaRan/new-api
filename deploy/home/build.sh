#!/usr/bin/env bash
set -euo pipefail
umask 077

commit=$(git rev-parse HEAD)
[[ "$commit" == "$GITHUB_SHA" ]]
version="ran-${commit:0:9}"
image="new-api:$version"
builds=/data/new-api-ran-builds
release="$builds/$commit"
receipt="release-$version/release.json"

if [[ -f "$release/$receipt" ]]; then
  # A rerun reuses the verified image for this exact commit.
  recorded_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image_id"])' "$release/$receipt")
  [[ "$(docker image inspect --format '{{.Id}}' "$image")" == "$recorded_id" ]]
else
  work=$(mktemp -d "$builds/.$version.XXXXXXXX")
  temporary_image="new-api:home-build-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  cleanup() {
    docker image rm "$temporary_image" >/dev/null 2>&1 || true
    rm -rf -- "$work"
  }
  trap cleanup EXIT
  # The running version is needed for the existing rolling-release verification.
  runtime_base=$(docker inspect --format '{{.Config.Image}}' new-api-ran-app)
  runtime_id=$(docker inspect --format '{{.Image}}' new-api-ran-app)
  [[ "$runtime_base" != "$image" && -z "$(docker image ls -q "$image")" ]]
  manifest=$(python3 deploy/home/build_receipt.py prepare "$work" "$commit")
  web_tree=$(git rev-parse 'HEAD:web')
  args=(--platform linux/amd64 --build-arg http_proxy --build-arg https_proxy --build-arg no_proxy
        --build-arg "WEB_BUILD_VERSION=ran-web-${web_tree:0:9}" --build-arg "BUILD_VERSION=$version")
  docker buildx build "${args[@]}" --load -t "$temporary_image" \
    --label "org.opencontainers.image.source=$GITHUB_SERVER_URL/$GITHUB_REPOSITORY" \
    --label "org.opencontainers.image.revision=$commit" --label "org.opencontainers.image.version=$version" \
    --label "io.new-api.source-manifest-sha256=$manifest" --label 'io.new-api.frontend-base-path=/' "$work/candidate"
  docker buildx build "${args[@]}" --target web-dist --output "type=local,dest=$work/dist" "$work/candidate"
  python3 deploy/home/build_receipt.py finish "$work" --dist "$work/dist" --built-image "$temporary_image" \
    --runtime-base "$runtime_base" --runtime-base-id "$runtime_id"
  rm -rf -- "$work/dist"
  docker tag "$temporary_image" "$image"
  mv -T "$work" "$release"
fi

printf 'Image: `%s`\n\nBuild receipt on 110: `%s/%s`\n' "$image" "$release" "$receipt" >> "$GITHUB_STEP_SUMMARY"
