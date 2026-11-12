# Install

Install SpaceMosquito from a [GitHub Release](https://github.com/infomosquitos/SpaceMosquito/releases).

## Requirements

- A release archive for your OS/arch (`spacemosquito-<os>-<arch>.tar.gz`, `.zip` on Windows)
- Firefox or Chrome (for the Space Mosquito extension)
- About 1 GB of disk for semantic search (the bge-m3 model, 635 MB, plus libraries)

## Homebrew (macOS)

```sh
brew install --cask infomosquitos/tap/spacemosquito
```

Homebrew 7 asks you to trust casks from third-party taps once:
`brew trust --cask infomosquitos/tap/spacemosquito` (the error message shows the
command).

The install also downloads the semantic search model (bge-m3, 635 MB) into
`$(brew --prefix)/share/spacemosquito/models`, once; upgrades keep it. If that
download fails (e.g. offline), install still succeeds with keyword search, and
you can fetch the model later:
`spacemosquito model install --dir "$(brew --prefix)/share/spacemosquito/models"`.

Upgrade with `brew upgrade --cask spacemosquito`. Uninstall with
`brew uninstall --cask spacemosquito`; add `--zap` to also delete
`~/.spacemosquito` (catalog, config and encryption key) and the model.

The cask installs the CLI only — install the
[browser extension](#browser-extension) separately. Linux and Windows: use the
archive below.

## Archive (Linux, Windows, macOS without Homebrew)

1. Download the archive for your platform and `SHA256SUMS` from the release,
   and verify the checksum.
2. Unpack it into a folder you keep. It holds `spacemosquito`,
   `spacemosquito-embedder` and `lib/` (llama.cpp); keep them together.
3. Put `spacemosquito` on your `PATH` and download the semantic search model
   (bge-m3, 635 MB, MIT license; sha256-checked) into a `models` folder next to
   the binary (or the default, `~/.spacemosquito/models`):

   ```sh
   mkdir -p ~/opt/spacemosquito && tar xzf spacemosquito-linux-amd64.tar.gz -C ~/opt/spacemosquito
   ln -s ~/opt/spacemosquito/spacemosquito ~/.local/bin/spacemosquito
   spacemosquito model install --dir ~/opt/spacemosquito/models
   ```

   Without the model, search is keyword-only. `spacemosquito stats` shows
   whether semantic search found its files.
4. macOS without Homebrew: remove the quarantine flag once,
   `xattr -dr com.apple.quarantine ~/opt/spacemosquito`.

## Browser extension

Each release also includes:

- `spacemosquito-firefox-*.xpi` — signed Firefox add-on
- `spacemosquito-chrome-*.zip` — Chrome unpacked package

### Firefox

1. Download `spacemosquito-firefox-*.xpi` from the release.
2. Open `about:addons` → gear icon → **Install Add-on From File…**
3. Select the `.xpi` file and confirm.

The add-on stays installed across Firefox restarts.

### Chrome

Chrome loads the extension from a **folder**, not from the `.zip` (selecting the
zip fails with *Manifest file is missing or unreadable*).

1. Unzip `spacemosquito-chrome-*.zip` into a folder you keep — Chrome reads it
   on every start, so the extension breaks if the folder is deleted:

   ```sh
   mkdir -p ~/Applications/SpaceMosquito-Chrome
   unzip -o ~/Downloads/spacemosquito-chrome-v*.zip -d ~/Applications/SpaceMosquito-Chrome
   ```

2. Open `chrome://extensions` → enable **Developer mode** → **Load unpacked**
3. Select that folder (the one that contains `manifest.json`). In the macOS file
   picker, **Cmd+Shift+G** lets you type the path.

To upgrade, unzip the new release into the same folder (`-o` overwrites), then
click the reload icon on the extension card in `chrome://extensions`.

## Next steps

```sh
spacemosquito init
spacemosquito serve
```

Then capture a Confluence session with the extension (see [README](../README.md#capture-a-confluence-session)).
