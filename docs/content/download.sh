#!/usr/bin/env bash
set -euo pipefail

# ── Vibepit downloader ──────────────────────────────────────────────
# Downloads the latest vibepit binary from GitHub releases into the
# current directory.
# Usage:
#   curl -fsSL https://vibepit.dev/download.sh -o download.sh
#   less download.sh   # inspect first
#   bash download.sh

REPO="bernd/vibepit"
INTERACTIVE=true

TMPDIR_DL=$(mktemp -d) || { echo "Failed to create temporary directory." >&2; exit 1; }
trap '[ -n "$TMPDIR_DL" ] && rm -rf "$TMPDIR_DL"' EXIT

# ── Parse flags ─────────────────────────────────────────────────────

for arg in "$@"; do
    case "$arg" in
        --non-interactive) INTERACTIVE=false ;;
    esac
done

# ── Color support ───────────────────────────────────────────────────

setup_colors() {
    if command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
        CYAN='\033[38;2;0;212;255m'     # #00d4ff
        ORANGE='\033[38;2;249;115;22m'  # #f97316
        FIELD='\033[38;2;0;153;204m'    # #0099cc
        BOLD='\033[1m'
        ITALIC='\033[3m'
        RESET='\033[0m'
    else
        CYAN='' ORANGE='' FIELD='' BOLD='' ITALIC='' RESET=''
    fi
}
setup_colors

# ── Output helpers ──────────────────────────────────────────────────

info()  { printf "${CYAN}%s${RESET}\n" "$*"; }
error() { printf "${ORANGE}%s${RESET}\n" "$*" >&2; exit 1; }

# ── HTTP fetch helper ─────────────────────────────────────────────

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$@"
    elif command -v wget >/dev/null 2>&1; then
        # Translate curl-style "-o FILE URL" into wget-style "-O FILE URL".
        local args=()
        while [ $# -gt 0 ]; do
            case "$1" in
                -o) args+=("-O" "$2"); shift 2 ;;
                *)  args+=("$1"); shift ;;
            esac
        done
        wget -q "${args[@]}"
    else
        error "Either curl or wget is required."
    fi
}

# ── Header ──────────────────────────────────────────────────────────

print_header() {
    local field="${FIELD}╱${RESET}"
    local name="${CYAN}${BOLD}VIBEPIT${RESET}"
    local tagline="${ORANGE}${ITALIC}I pity the vibes${RESET}"

    printf '\n%b%b%b %b  %b  %b%b%b\n\n' \
        "$field" "$field" "$field" "$name" "$tagline" "$field" "$field" "$field"
}

# ── Platform detection ──────────────────────────────────────────────

detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux*)  os="linux" ;;
        Darwin*) os="darwin" ;;
        *)       error "Unsupported OS: $(uname -s)" ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)  arch="x86_64" ;;
        arm64|aarch64) arch="aarch64" ;;
        *)             error "Unsupported architecture: $(uname -m)" ;;
    esac

    PLATFORM_OS="$os"
    PLATFORM_ARCH="$arch"
}

# ── Fetch latest release tag ────────────────────────────────────────

extract_tag() {
    grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/'
}

fetch_latest_version() {
    local api="https://api.github.com/repos/${REPO}/releases"
    local response

    response=$(fetch "${api}/latest" 2>/dev/null) || response=""
    VERSION=$(printf '%s' "$response" | extract_tag) || true

    # Fall back to the newest release (including pre-releases) if no latest exists.
    if [ -z "$VERSION" ]; then
        response=$(fetch "$api") || error "Failed to fetch release info from GitHub."
        VERSION=$(printf '%s' "$response" | extract_tag)
    fi

    [ -n "$VERSION" ] || error "Could not determine latest version."

    # Strip leading 'v' for the archive filename
    VERSION_NUM="${VERSION#v}"
}

# ── Download and extract ────────────────────────────────────────────

download() {
    local dest="$1"
    local url="$2"
    local archive="$3"
    local archive_dir="$4"
    local checksums_url
    checksums_url="$(dirname "$url")/checksums.txt"

    info "Downloading ${archive}..."
    fetch -o "${TMPDIR_DL}/${archive}" "$url"             || error "Download failed: ${url}"
    fetch -o "${TMPDIR_DL}/checksums.txt" "$checksums_url" || error "Download failed: ${checksums_url}"

    info "Verifying checksum..."
    local expected actual
    expected=$(grep "${archive}" "${TMPDIR_DL}/checksums.txt" | awk '{print $1}')
    [ -n "$expected" ] || error "Archive not found in checksums.txt."

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "${TMPDIR_DL}/${archive}" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "${TMPDIR_DL}/${archive}" | awk '{print $1}')
    else
        error "sha256sum or shasum is required for checksum verification."
    fi

    [ "$expected" = "$actual" ] || error "Checksum mismatch: expected ${expected}, got ${actual}"

    info "Extracting..."
    tar -xzf "${TMPDIR_DL}/${archive}" -C "$TMPDIR_DL" || error "Extraction failed."

    local binary="${TMPDIR_DL}/${archive_dir}/vibepit"
    [ -f "$binary" ] || error "Expected binary not found at ${archive_dir}/vibepit in archive."

    cp "$binary" "${dest}/vibepit" || error "Failed to write binary to ${dest}"
    chmod +x "${dest}/vibepit"
}

# ── Main ────────────────────────────────────────────────────────────

main() {
    print_header

    detect_platform
    info "Detected platform: ${PLATFORM_OS}/${PLATFORM_ARCH}"

    fetch_latest_version
    info "Latest release: ${VERSION}"
    echo

    local dest
    dest=$(pwd)

    local archive_dir="vibepit-${VERSION_NUM}-${PLATFORM_OS}-${PLATFORM_ARCH}"
    local archive="${archive_dir}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"

    printf "  %bURL%b  %s\n" "$BOLD" "$RESET" "$url"
    printf "  %bTo%b   %s/vibepit\n" "$BOLD" "$RESET" "$dest"
    echo

    if $INTERACTIVE; then
        local reply
        if [ -f "${dest}/vibepit" ]; then
            printf "%bvibepit already exists in %s. Overwrite? [y/N]%b " "$ORANGE" "$dest" "$RESET"
            read -r reply </dev/tty
            case "$reply" in [Yy]|[Yy]es) ;; *) info "Download cancelled."; exit 0 ;; esac
        else
            printf "Proceed? [Y/n] "
            read -r reply </dev/tty
            case "$reply" in ""|[Yy]|[Yy]es) ;; *) info "Download cancelled."; exit 0 ;; esac
        fi
        echo
    fi

    download "$dest" "$url" "$archive" "$archive_dir"

    echo
    printf "%bDownloaded vibepit %s to %s/%bvibepit%b\n" "$CYAN" "$VERSION" "$dest" "$ORANGE" "$RESET"
    info "Move it to a directory in your PATH to use it, for example:"
    echo
    printf "  %bsudo mv %s/vibepit /usr/local/bin/%b\n" "$BOLD" "$dest" "$RESET"
    echo
}

main "$@"
