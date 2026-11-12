# Release

Related: [CHANGELOG.md](../CHANGELOG.md), [CLEANUP.md](CLEANUP.md)

Promote finished task artifacts with [CLEANUP.md](CLEANUP.md) first if needed (backlog rows, task detail files, STATUS). This runbook is for **versioning and publishing**.

## Prepare

1. Ensure the build is green: `make test`
2. Update [`CHANGELOG.md`](../CHANGELOG.md) to the new version (move items out of Unreleased). Keep changelog entries concise, focus on user-facing functionality changes, skip technical details.
3. Confirm cleanup is done (or run [`CLEANUP.md`](CLEANUP.md) now).
4. Only when the extensions changed: bump firefox-extension and chrome-extension
   `manifest.json` + `package.json` to X.Y.Z (release CI also stamps the version into
   the packaged manifests). When nothing but version numbers changed under
   `firefox-extension/`, `chrome-extension/` and `scripts/build-extension-zips.sh`
   since the previous tag, the `build-extensions` job reuses the previous release's
   signed XPI and Chrome zip, renamed to this version, instead of signing again:
   AMO rate-limits signing (a day with many releases got "Request was throttled").
5. Ensure GitHub repository secrets `AMO_JWT_ISSUER` and `AMO_JWT_SECRET` are set
   (AMO API credentials for signing the Firefox XPI). Without them the
   `build-extensions` job fails.
6. Ensure GitHub repository secret `HOMEBREW_TAP_TOKEN` is set and not expired:
   a fine-grained PAT (resource owner `infomosquitos`, repository
   [`homebrew-tap`](https://github.com/infomosquitos/homebrew-tap) only,
   **Contents: Read and write**). Without it the `homebrew` job fails after the
   release is published; rotate it before it expires.

## Cut the release

```sh
git tag vX.Y.Z         # use the version from CHANGELOG
git push origin vX.Y.Z
```

Watch **Actions → Release**. It tests, builds four platform archives
(`spacemosquito/scripts/package.sh`: CLI, `spacemosquito-embedder`, pinned
llama.cpp libraries in `lib/`, licenses; the Linux job smoke-tests that the
bundled libraries load) + signed Firefox XPI + Chrome extension zip, uploads
them + `SHA256SUMS`, and creates the GitHub Release for that tag. Then the
`homebrew` job renders `Casks/spacemosquito.rb`
([`scripts/render-homebrew-cask.sh`](../scripts/render-homebrew-cask.sh)) and
`Casks/spacemosquito-bge-m3-embeddings-model.rb`
([`scripts/render-model-cask.sh`](../scripts/render-model-cask.sh); unchanged
unless the model changes) and pushes them to the tap (skipped for pre-release
tags containing `-`).

**llama.cpp / yzma bumps (ADR-019):** the libraries are the upstream build that
the yzma version in `go.mod` pins (`pkg/download.DefaultVersion`). Bump yzma,
then `LLAMA_TAG` and the four sha256 values in
`spacemosquito/scripts/fetch-llama-libs.sh` together, and run the real-model
test (`go test -tags=llama ./internal/embed`, see `dev/README.md`) before
tagging. **Model changes:** edit `scripts/render-model-cask.sh` (revision,
sha256, file, bump its `VERSION`) and `embed.ModelFile`; every page is
re-embedded once.

Alternative: GitHub **Releases → Draft a new release → create tag `vX.Y.Z` → Publish**. That also pushes the tag and triggers the same workflow (assets attach to the release).

## After

1. Confirm assets on the release page:
   - `spacemosquito-{darwin,linux}-*.tar.gz` and `spacemosquito-windows-amd64.zip` archives
   - `spacemosquito-firefox-v*.xpi` and `spacemosquito-chrome-v*.zip`
   - `SHA256SUMS` covering all of the above
2. Spot-check: Firefox — install XPI via `about:addons` → Install Add-on From File.
   Chrome — unzip so `manifest.json` is at the archive root, then Load unpacked.
3. Confirm the tap commit `spacemosquito X.Y.Z` landed in
   [`infomosquitos/homebrew-tap`](https://github.com/infomosquitos/homebrew-tap/commits),
   then spot-check: `brew update && brew upgrade --cask spacemosquito && spacemosquito version && spacemosquito stats`
   (the last line should show `Semantic Search: bge-m3-q8_0.gguf, …`).
4. Set [`STATUS.md`](STATUS.md) next focus (usually next backlog item).

## Local release artifacts (no GitHub)

Produces the same binary + extension layout as CI when AMO env vars are set:

```sh
cd spacemosquito
export AMO_JWT_ISSUER=… AMO_JWT_SECRET=…   # optional; without them Firefox XPI is skipped
./scripts/build-release.sh vX.Y.Z
ls dist/
```
