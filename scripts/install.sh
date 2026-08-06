#!/bin/sh
# Hearsay installer: https://hearsay.decypharr.com/install.sh
#
# Downloads the release binary for this system, verifies its checksum,
# installs it, and registers a service (systemd on Linux, launchd on
# macOS) that runs "hearsay serve".
#
#   HEARSAY_VERSION      install this tag instead of the latest release
#   HEARSAY_INSTALL_DIR  install here instead of /usr/local/bin
#   HEARSAY_NO_SERVICE   set to 1 to skip service registration
set -eu

REPO="sirrobot01/hearsay"
INSTALL_DIR="${HEARSAY_INSTALL_DIR:-/usr/local/bin}"

say() { printf 'hearsay: %s\n' "$1"; }
fail() { printf 'hearsay: error: %s\n' "$1" >&2; exit 1; }

latest_version() {
    curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/$REPO/releases/latest" | sed 's|.*/tag/||'
}

verify() {
    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$1" && grep " $2\$" checksums.txt | sha256sum -c - >/dev/null)
    else
        (cd "$1" && grep " $2\$" checksums.txt | shasum -a 256 -c - >/dev/null)
    fi
}

install_service_linux() {
    command -v systemctl >/dev/null 2>&1 || {
        say "systemd not found; start the daemon yourself: hearsay serve"
        return 0
    }
    unit=/etc/systemd/system/hearsay.service
    say "registering systemd service ($unit)"
    $SUDO sh -c "cat > $unit" <<EOF
[Unit]
Description=Hearsay daemon
After=network-online.target
Wants=network-online.target

[Service]
User=$(id -un)
ExecStart=$INSTALL_DIR/hearsay serve
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable hearsay
    # restart, not "enable --now": an already-running service must pick
    # up the new binary on upgrade
    $SUDO systemctl restart hearsay
    say "service running; check it with: systemctl status hearsay"
}

install_service_darwin() {
    plist="$HOME/Library/LaunchAgents/com.decypharr.hearsay.plist"
    say "registering launchd agent ($plist)"
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
    cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.decypharr.hearsay</string>
    <key>ProgramArguments</key>
    <array><string>$INSTALL_DIR/hearsay</string><string>serve</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>$HOME/Library/Logs/hearsay.log</string>
    <key>StandardErrorPath</key><string>$HOME/Library/Logs/hearsay.log</string>
</dict>
</plist>
EOF
    launchctl unload "$plist" 2>/dev/null || true
    launchctl load -w "$plist"
    say "agent running; logs: ~/Library/Logs/hearsay.log"
}

main() {
    command -v curl >/dev/null 2>&1 || fail "curl is required"

    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux | darwin) ;;
        *) fail "unsupported OS: $os (download the Windows zip from https://github.com/$REPO/releases)" ;;
    esac
    arch=$(uname -m)
    case "$arch" in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) fail "unsupported architecture: $arch" ;;
    esac

    version="${HEARSAY_VERSION:-$(latest_version)}"
    case "$version" in
        v[0-9]*) ;;
        *) fail "cannot determine the latest release (is one published?)" ;;
    esac
    archive="hearsay_${version#v}_${os}_${arch}.tar.gz"
    url="https://github.com/$REPO/releases/download/$version"

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    say "downloading $archive ($version)"
    curl -fsSL -o "$tmp/$archive" "$url/$archive"
    curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt"
    verify "$tmp" "$archive" || fail "checksum mismatch for $archive"
    tar -xzf "$tmp/$archive" -C "$tmp"

    SUDO=""
    if ! mkdir -p "$INSTALL_DIR" 2>/dev/null || [ ! -w "$INSTALL_DIR" ]; then
        [ "$(id -u)" -eq 0 ] || SUDO=sudo
        $SUDO mkdir -p "$INSTALL_DIR"
    fi
    say "installing to $INSTALL_DIR/hearsay"
    $SUDO install -m 755 "$tmp/hearsay" "$INSTALL_DIR/hearsay"
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) ;;
        *) say "note: $INSTALL_DIR is not in your PATH" ;;
    esac

    if [ "${HEARSAY_NO_SERVICE:-0}" = "1" ]; then
        say "skipping service registration; run: hearsay serve"
    elif [ "$os" = "linux" ]; then
        install_service_linux
    else
        install_service_darwin
    fi
    say "installed $("$INSTALL_DIR/hearsay" --version 2>/dev/null || echo "hearsay $version")"
    say "query it: curl 127.0.0.1:8477/v1/status"
}

main "$@"
