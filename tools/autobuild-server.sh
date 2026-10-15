#!/bin/bash
# iTaK Gaming auto-build (runs on the ubuntu-server via cron).
#
# Watches the GitHub repo's main branch. When a push raises the
# pubspec version above the published one, this script rebuilds
# the APK with the server toolchain and publishes it to the app
# update feed (~/itak-netplay/app), where the app's self-updater
# picks it up. Publishing is the only outward effect; a failed
# build leaves the published version untouched.
#
# Invoked through ~/itak-autobuild.sh, a stable wrapper that
# refreshes the clone and runs a copy of this file so a push can
# never replace the script mid-run.
set -u
export PATH=/usr/local/bin:/usr/bin:/bin

CLONE=$HOME/itak-repo
WORK=$HOME/itak-gaming
APPDIR=$HOME/itak-netplay/app
STATE=$HOME/itak-autobuild.state
LOG=$HOME/itak-autobuild.log
exec >>"$LOG" 2>&1

echo "=== $(date -Is)"

state_get() { grep "^$1=" "$STATE" 2>/dev/null | cut -d= -f2-; }
state_set() {
    local tmp="$STATE.tmp"
    grep -v "^$1=" "$STATE" 2>/dev/null > "$tmp" || true
    echo "$1=$2" >> "$tmp"
    mv "$tmp" "$STATE"
}

SHA=$(git -C "$CLONE" rev-parse HEAD)
LAST=$(state_get LAST_SHA)
if [ "$SHA" = "$LAST" ]; then
    echo "no change: $SHA"
    exit 0
fi

# A failed build for this commit retries at most every 30 min.
FAIL_SHA=$(state_get FAIL_SHA)
FAIL_EPOCH=$(state_get FAIL_EPOCH)
NOW=$(date +%s)
if [ "$SHA" = "$FAIL_SHA" ] && [ -n "$FAIL_EPOCH" ] && [ $((NOW - FAIL_EPOCH)) -lt 1800 ]; then
    echo "build for $SHA failed recently; backing off"
    exit 0
fi
fail() {
    state_set FAIL_SHA "$SHA"
    state_set FAIL_EPOCH "$(date +%s)"
    echo "FAILED: $1"
    exit 0
}

# Version gate: only a pubspec version bump ships an update.
VERLINE=$(grep '^version:' "$CLONE/flutter_ui/pubspec.yaml")
VNAME=$(echo "$VERLINE" | sed 's/^version: //; s/+.*$//')
VCODE=$(echo "$VERLINE" | sed 's/^.*+//')
PUBCODE=$(python3 -c "import json; print(json.load(open('$APPDIR/version.json'))['versionCode'])" 2>/dev/null || echo 0)
echo "repo version $VNAME+$VCODE, published code $PUBCODE"
if ! [ "$VCODE" -gt "$PUBCODE" ] 2>/dev/null; then
    state_set LAST_SHA "$SHA"
    echo "not a version bump; marked seen"
    exit 0
fi

# Sync sources into the build copy. Binary-only paths that git
# does not carry (emulator cores, the Go AAR) are preserved.
rsync -a --delete \
    --exclude '.git' \
    --exclude 'flutter_ui/build' \
    --exclude 'flutter_ui/.dart_tool' \
    --exclude 'flutter_ui/android/app/src/main/assets/cores' \
    --exclude 'flutter_ui/android/app/libs/itak_backend.aar' \
    --exclude 'testdata' \
    "$CLONE/" "$WORK/" || fail "rsync"

# Rebind the Go backend when it or the bundled cores changed.
# The cores are linked into the backend as native packages, so a
# core-only fix must rebind too; hashing go_backend alone would
# silently ship the stale AAR.
export JAVA_HOME=$HOME/toolchain/jdk17
export PATH=$JAVA_HOME/bin:$HOME/toolchain/go/bin:$HOME/go/bin:/usr/bin:/bin
export ANDROID_NDK_HOME=$HOME/toolchain/android-sdk/ndk/26.1.10909125
export ANDROID_HOME=$HOME/toolchain/android-sdk
AAR_TREE=$(git -C "$CLONE" rev-parse HEAD:go_backend HEAD:cores | sha256sum | cut -d" " -f1)
if [ "$AAR_TREE" != "$(state_get AAR_TREE)" ] || [ ! -f "$WORK/flutter_ui/android/app/libs/itak_backend.aar" ]; then
    echo "go_backend changed; rebinding AAR"
    (cd "$WORK/go_backend" && gomobile bind -target=android/arm -androidapi 24 \
        -o "$WORK/flutter_ui/android/app/libs/itak_backend.aar" ./mobile) \
        || fail "gomobile bind"
    cp "$WORK/flutter_ui/android/app/libs/itak_backend.aar" "$WORK/builds/itak_backend.aar" 2>/dev/null || true
    state_set AAR_TREE "$AAR_TREE"
fi

# Build the APK.
export PATH=$JAVA_HOME/bin:$HOME/toolchain/flutter/bin:/usr/bin:/bin
(cd "$WORK/flutter_ui" && env XDG_CONFIG_HOME=$HOME/toolchain/xdg \
    flutter build apk --release --target-platform android-arm) \
    || fail "flutter build"

# Publish: versioned APK + fresh manifest, then prune old ones.
APK="$WORK/flutter_ui/build/app/outputs/flutter-apk/app-release.apk"
[ -f "$APK" ] || fail "APK missing after build"
OUT="$APPDIR/iTaKGaming-$VNAME.apk"
cp "$APK" "$OUT" || fail "publish copy"
python3 - "$OUT" "$VNAME" "$VCODE" "$CLONE/release-notes.txt" "$APPDIR/version.json" <<'PYEOF' || fail "manifest write"
import hashlib, json, sys, os, datetime
apk, vname, vcode, notes_path, out_path = sys.argv[1:6]
data = open(apk, "rb").read()
notes = "A new build of iTaK Gaming."
if os.path.exists(notes_path):
    txt = open(notes_path).read().strip()
    if txt:
        notes = txt
manifest = {
    "versionCode": int(vcode),
    "versionName": vname,
    "apk": os.path.basename(apk),
    "sha256": hashlib.sha256(data).hexdigest(),
    "size": len(data),
    "notes": notes,
    "publishedAt": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
}
tmp = out_path + ".tmp"
open(tmp, "w").write(json.dumps(manifest))
os.replace(tmp, out_path)
print("manifest:", json.dumps(manifest))
PYEOF
ls -t "$APPDIR"/iTaKGaming-*.apk 2>/dev/null | tail -n +4 | xargs -r rm -f

state_set LAST_SHA "$SHA"
state_set FAIL_SHA ""
echo "published $VNAME ($SHA)"
