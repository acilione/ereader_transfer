#!/bin/sh
# Native receiver; qndb comes from the separate NickelDBus installation.
BASE=/mnt/onboard/.adds/pageport
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin
if [ ! -f "$BASE/config.json" ]; then
    qndb -m mwcToast 5000 'Pageport: copy config.json into .adds/pageport first.' ''
    exit 1
fi
if ! command -v qndb >/dev/null 2>&1; then
    exit 1
fi
if pgrep -f '^/mnt/onboard/.adds/pageport/pageport-receiver' >/dev/null 2>&1; then
    qndb -m mwcToast 3000 'Pageport receiver is already running.' ''
    exit 0
fi
chmod +x "$BASE/pageport-receiver"
"$BASE/pageport-receiver" --config "$BASE/config.json" > "$BASE/receiver.log" 2>&1 &
RECEIVER_PID=$!
sleep 1
if kill -0 "$RECEIVER_PID" 2>/dev/null; then
    qndb -m mwcToast 4000 'Pageport is ready. Keep Wi-Fi on and the Kobo awake.' ''
else
    qndb -m mwcToast 6000 'Pageport could not start. Check .adds/pageport/receiver.log.' ''
fi
