# Pageport: send from the computer, accept on Kobo

This is an **experimental native receiver**, targeting the Kobo Aura on firmware **4.38.23852** and the NickelDBus 0.2.0 API. The protocol, native-dialog command handling, and filesystem behavior are tested on a computer. **Actual operation on a physical Kobo has not yet been verified.** A firmware version match is not a hardware test.

The stock Kobo firmware does not provide Pageport with a general incoming-file confirmation API. This method therefore needs a one-time USB installation of NickelMenu, NickelDBus, and the Pageport receiver. The desktop app remains portable. The existing browser transfer method remains available without these additions.

Once the receiver is running: select one or more books in Pageport, click **Send selected**, then tap **Accept** in a native Kobo dialog. No Kobo browser or website is opened. Reject or closing the dialog transfers no book contents.

## One-time setup

1. Extract the desktop Pageport ZIP into a permanent, writable folder. Start Pageport from that folder; do not launch it from inside the ZIP. Preserve `pageport-data` to retain pairings and your library.
2. Install [NickelMenu](https://github.com/pgaskin/NickelMenu/releases) following its upstream instructions: copy its `KoboRoot.tgz` into `.kobo/`, then safely eject and let the Kobo install/reboot. Firmware 5.x/6.x is outside this receiver's scope.
3. Separately install [NickelDBus 0.2.0](https://github.com/shermp/NickelDBus/releases/tag/0.2.0) the same way. Do not overwrite an existing newer NickelDBus installation with this older version. NickelDBus supplies the `qndb` command and native dialogs.
4. Connect the Kobo over USB again. Extract `Pageport-kobo-receiver.zip` and copy its `.adds` folder to the root of the Kobo storage. Merge folders; the package adds `.adds/pageport/` and one separate `.adds/nm/pageport` menu file. Preserve other menu/configuration files.
5. In Pageport's **Send straight to your Kobo** panel, expand setup, choose a name, and click **Create pairing**. Download that device's configuration, and place `config.json` in `.adds/pageport/` beside `pageport-receiver`. If several network adapters exist, choose the correct Wi-Fi address under **Connect your Kobo** before downloading the configuration.
6. Safely eject the Kobo, enable Wi-Fi, and tap **NickelMenu → Start Pageport receiver**. The portal should show **receiver online**. Select up to 20 files and click **Send selected**. Tap **Accept** on the Kobo.

NickelMenu and NickelDBus are separate community projects; their binaries are not bundled or automatically installed by Pageport. Installing their KoboRoot packages changes software on the Kobo. Installation is performed by you over USB after reviewing their instructions.

## During use

- Keep the computer and Kobo on the same trusted Wi-Fi network, with Pageport open and the Kobo awake. There is no remote wake-up. Sleep, disabled Wi-Fi, and client-isolated guest networks prevent delivery.
- Start the receiver again after a Kobo reboot or USB connection. It stops on NickelDBus's USB-connect signal so it will not continue writing while the computer accesses the onboard storage. This version deliberately does not modify boot scripts or auto-start at power-on.
- The native prompt expires after three minutes. One batch may be pending per paired Kobo. You can cancel a queued or not-yet-accepted transfer in the portal.
- Accepted files are streamed, checked against their exact byte size and SHA-256, and saved in `Pageport/` on the Kobo. Their filenames receive a short ID suffix to avoid overwriting other books. A retry of the same file reuses a matching existing copy.
- The receiver then requests a native library rescan. **Received · library scan completed** means the files were verified and a scan-complete signal arrived; it does not guarantee that a malformed or DRM-protected book is readable. **Saved · library scan needs attention** means the files arrived but import could not be confirmed; choose **Import Pageport books** from NickelMenu.
- If a batch is interrupted, some files may already have been saved. Retry the batch. Verified existing files are reused. A partial output file left by sudden power loss must be removed manually before retrying that same file.
- Restarting the desktop app marks incomplete transfers failed, preserving device pairings. Send again after the receiver reconnects. Deleting a library file is blocked while a pending transfer uses it.
- This does not repair EPUBs or remove DRM. If a book has format errors, repair and validate a copy first (see the EPUB troubleshooting notes in the main README).

## Pairing and network changes

The configuration contains a private, random device credential. It can fetch only transfers addressed to that device, and only after the receiver has acknowledged acceptance. Pageport exposes management controls only to the host computer. The receiver is an outbound client; it opens no listening port on the Kobo.

This local protocol uses HTTP for compatibility with old firmware. Use a trusted home network and do not expose the server to the internet. Do not commit or share `config.json`. To revoke a credential, choose **Unpair** in Pageport. To pair again, create a new pairing and replace the configuration.

The configuration contains the computer's LAN address and port. If either changes, download the configuration again. A router DHCP reservation can keep the address stable. Changing the browser's six-digit session code does not change receiver pairings.

## Troubleshooting and removal

- **No online receiver:** check Wi-Fi, the configuration's server address/port, Windows Firewall's private-network access, and `.adds/pageport/receiver.log`.
- **Start fails:** confirm firmware 4.x, working NickelDBus with `qndb`, and its `pfmAboutToConnect` signal. The receiver refuses to run without USB monitoring. Logs do not intentionally include the pairing credential.
- **No Accept dialog:** keep the Kobo awake; do not run another NickelDBus dialog application at the same time. An unexpected `qndb` response never counts as acceptance.
- **Stop:** choose **Stop Pageport receiver** in NickelMenu.
- **Remove:** stop the receiver, connect USB, remove only `.adds/pageport/` and `.adds/nm/pageport`, then **Unpair** in the portal. Saved books under `Pageport/` remain. NickelMenu and NickelDBus have their own upstream uninstall instructions.

## Build

The receiver must use **Go 1.23.x or earlier** because Go 1.24+ requires Linux 3.2+, while older Aura devices have older kernels. The provided receiver is built with Go **1.23.12**, `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=arm`, `GOARM=5`. This toolchain is no longer maintained; it is used only for this explicitly experimental legacy-device component. Current Go builds the desktop applications.

```sh
python3 scripts/build_receiver.py --go /path/to/go1.23.12/bin/go
```

The helper checks the toolchain version, creates `dist/Pageport-kobo-receiver.zip`, and writes a SHA-256 file. It never writes to a connected Kobo. The receiver uses only Go's standard library plus NickelDBus on the device.

Protocol and acceptance integration tests: `go test -race ./...`. These use fake native UI callbacks and local servers; they are not hardware tests. The qndb parser test uses the upstream 0.2.0 output format `dlgConfirmResult 1` / `dlgConfirmResult 0`.

References: [NickelDBus API](https://shermp.github.io/NickelDBus/ndb-ndbdbus.html), [NickelDBus CLI](https://shermp.github.io/NickelDBus/), [Go kernel requirements](https://go.dev/wiki/MinimumRequirements).
