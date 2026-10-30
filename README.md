# Pageport

Send books to your Kobo over your home Wi-Fi, using a portable app and your Kobo's built-in browser.

Pageport opens a local website on your computer. Drop your books into the website, open the displayed address on your Kobo, and tap a book to download it. Your files stay on your computer and local network. There is no account, cloud service, USB cable, or extra Kobo software to install.

## Run the portable app

Choose the build for your computer from `dist/`:

| Computer | Archive |
| --- | --- |
| Windows, Intel or AMD | `Pageport-windows-amd64.zip` |
| Windows on ARM | `Pageport-windows-arm64.zip` |
| macOS, Apple silicon | `Pageport-macos-arm64.zip` |
| macOS, Intel | `Pageport-macos-amd64.zip` |
| Linux, Intel or AMD | `Pageport-linux-amd64.zip` |
| Linux, ARM64 | `Pageport-linux-arm64.zip` |

Extract the archive to a writable folder before running it. No Python, Go, Node.js, or other runtime is needed to run a packaged build.

- **Windows:** double-click `Pageport.exe`. If Windows Firewall asks, allow access on your private home network. Run the Windows executable directly, rather than through WSL, so the displayed address belongs to your computer's normal network.
- **macOS:** run `./Pageport` from Terminal. These builds are unsigned, so macOS may require approval in System Settings → Privacy & Security.
- **Linux:** run `./Pageport` from a terminal. If necessary, first run `chmod +x Pageport`.

The app opens your default browser. Keep the app and computer running while downloading. Closing its terminal window or pressing Ctrl+C stops the server.

## Transfer a book

1. Connect your computer and Kobo to the same Wi-Fi network.
2. Open Pageport on your computer and add your files.
3. On your Kobo, open **Beta Features → Web Browser**, usually under **More** or **Settings**, depending on firmware.
4. Type the network address displayed by Pageport, including `http://` and the port, and enter the session code. You can also type the complete reader link.
5. Tap a book's download link on the Kobo and accept its download prompt. Once imported, find the book in **My Books**.

The original **Kobo Aura**, Aura HD, Aura H2O, and Aura Edition 2 are listed by Kobo as having a [built-in web browser](https://help.kobo.com/hc/en-us/articles/360017763733-About-Beta-Features). The reader page uses plain HTML and large buttons, with no JavaScript required.

Transfers are started on the Kobo: Pageport makes files available, and the reader downloads them. The computer cannot silently push a book into an unmodified Kobo. A completed server download also cannot confirm that the Kobo imported the book successfully.

## File support

Start with DRM-free **EPUB** or **PDF** files. Pageport also accepts KEPUB, CBZ, CBR, TXT, RTF, HTML, MOBI, JPEG, PNG, GIF, BMP, and TIFF. Files are transferred as supplied; Pageport does not convert them. A `.kepub` filename is given the `.kepub.epub` ending needed by Kobo's browser.

Kobo's [supported file formats](https://help.kobo.com/hc/en-us/articles/360017763713-File-formats-your-Kobo-eReader-and-Kobo-Books-app-support) describe what the reader can open. Its experimental browser may download a narrower set, depending on the model and firmware. If a format does not download or import, try EPUB or PDF; changing an extension is not a format conversion. Physical Kobo hardware testing is still required to confirm behavior on a particular firmware.

The maximum upload size is **512 MiB per file**. ACSM files are Adobe download licenses, not books; use Adobe Digital Editions for Adobe DRM-protected titles as described in Kobo's format documentation.

## Your library and connection

- Uploaded copies and their metadata are stored in `pageport-data/` beside the executable. Keep this folder with the app when moving it. Removing a book in Pageport removes this copy, not the original file or an existing copy on your Kobo.
- Only one running instance can use a library folder at a time. To run another instance, choose a different folder with `--data` and a different port with `--port`.
- The desktop management interface is available only on the computer running Pageport. Other devices receive the simple reader page and need the session code.
- A new session code is generated when the app starts. You can also change it in the app. Pausing sharing prevents reader access while keeping your library.
- Pageport uses unencrypted HTTP for older Kobo browser compatibility. Use it on a trusted local network; do not forward its port to the internet.

## Connection troubleshooting

- **The Kobo cannot open the page:** keep Pageport running, check both devices are on the same network, include `http://` in the address, and allow Pageport through the computer's firewall for your private network.
- **Several addresses are listed:** try the address belonging to your Wi-Fi or Ethernet connection. VPN and virtual-machine adapters may also appear.
- **Guest or public Wi-Fi:** these networks often block communication between devices. Use your normal home network or a hotspot that permits local communication.
- **The address changed:** DHCP can change a computer's address after reconnecting. Use the current address shown by Pageport.
- **A download does nothing:** try a small DRM-free EPUB or PDF. Other file formats and browser behavior depend on firmware. Confirm sharing is enabled and refresh the reader page after changing the session code.
- **A generated EPUB reports an Adobe DRM error:** inspect the actual file before assuming it is encrypted. Compare the original and downloaded files' SHA-256 hashes to check the transfer, then run the official [EPUBCheck validator](https://github.com/w3c/epubcheck) locally. Generated EPUBs can contain invalid XML encoding declarations (use `UTF-8`, not `utf8`) or HTML5 elements such as `figure` inside an EPUB 2 publication that requires XHTML 1.1. Repair a separate copy and validate it again. Kobo also recommends validation in its [EPUB guidelines](https://github.com/kobolabs/epub-spec). The presence of `encryption.xml` alone is inconclusive because it can describe font obfuscation.
- **The browser did not open:** open `http://localhost:8080` on the host computer. If using a different port, use that port instead.
- **Port 8080 is already in use:** start Pageport with `--port 8081` and use the address it prints.
- **Data cannot be saved:** extract the app into a writable folder, or choose another data folder using `--data`.

## Command-line options

```text
Pageport --port 8080 --bind 0.0.0.0 --data /path/to/library --no-browser
Pageport --version
```

| Option | Purpose |
| --- | --- |
| `--port 8080` | HTTP port to listen on; `0` chooses an available port. |
| `--bind 0.0.0.0` | Listen on all IPv4 interfaces so the Kobo can connect. Binding to `127.0.0.1` makes the app accessible only on the host computer. |
| `--data PATH` | Store the library in this folder instead of beside the executable. |
| `--no-browser` | Start the server without opening the desktop browser. |
| `--version` | Print the version and exit. |

## Develop and build

The app uses Go's standard library and embeds its HTML, CSS, and JavaScript in the executable. There are no runtime or frontend package dependencies.

With Go 1.18 or later:

```sh
go run . --data ./pageport-data
go test ./...
```

To produce all six portable builds, install Python 3 and Go on the **build machine**, then run:

```sh
python scripts/build.py --version 0.1.0
```

To build only for Windows:

```sh
python scripts/build.py --target windows-amd64 --version 0.1.0
```

The build script runs the Go tests, cross-compiles self-contained executables, creates ZIP bundles containing the app and this guide, and writes `dist/SHA256SUMS`. Repeat `--target` to select multiple platforms. Use `--skip-tests` only when the same source has already passed tests.

When building on Windows from a network path such as a WSL checkout, the script automatically stages the sources in a temporary local folder so Go's module locking works, then copies the artifacts to `dist/` and cleans up the temporary folder.

The build artifacts are unsigned. Signing and notarization can be added when distributing the app more broadly. Builds for other platforms can be cross-compiled, but should be launched and tested on those operating systems before claiming full platform verification.

## Browser and concurrency checks

`go test ./...` checks the server without a browser. For concurrent request validation, run `go test -race ./...` on a platform with Go's race detector and its required C toolchain available.

The browser checks in `tests/browser.cjs` require Node.js, the Playwright package, and a Chromium browser installation on the **test machine**. They do not add runtime requirements to Pageport. Set `PLAYWRIGHT_MODULE` to an existing Playwright package path if it is not available through Node's normal module resolution. Optionally set `PLAYWRIGHT_BROWSER` to an existing Chromium executable; otherwise Playwright uses its installed browser.

Start a separate app instance with a **new, empty data directory** and a spare port, for example:

```sh
go run . --data ./.qa/browser-data-new --port 18087 --no-browser
```

On Windows you can launch `dist\Pageport-windows-amd64.exe` with those same options instead, which also avoids Go's source-path restrictions when the checkout is in WSL.

In a second terminal, run:

```sh
PAGEPORT_URL=http://127.0.0.1:18087 node tests/browser.cjs
```

On PowerShell, use:

```powershell
$env:PAGEPORT_URL = 'http://127.0.0.1:18087'
node tests/browser.cjs
```

The script checks desktop and mobile layouts, uploads, filters, sharing controls, deletion, and reader downloads with JavaScript disabled. It uploads synthetic transport fixtures, removes a test file, pauses sharing, and rotates the session code in the app instance selected by `PAGEPORT_URL`. **Do not point it at your personal library.** The files are not valid ebooks for Kobo import, and the test leaves some fixtures behind. Stop the test instance afterward and use a fresh empty data directory for a repeat run.

Screenshots are saved in `.qa/` by default; set `PAGEPORT_QA_DIR` to choose a different output folder. These browser checks do not replace a transfer test on a physical Kobo.
