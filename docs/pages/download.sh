#!/usr/bin/env bash
set -euo pipefail

# ── Vibepit downloader ──────────────────────────────────────────────
# Downloads the latest vibepit binary from GitHub releases into the
# current directory.
# Usage: curl -fsSL https://raw.githubusercontent.com/bernd/vibepit/main/download.sh | bash

REPO="bernd/vibepit"
INTERACTIVE=true

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
        PURPLE='\033[38;2;139;92;246m'  # #8b5cf6
        ORANGE='\033[38;2;249;115;22m'  # #f97316
        FIELD='\033[38;2;0;153;204m'    # #0099cc
        BOLD='\033[1m'
        ITALIC='\033[3m'
        RESET='\033[0m'
    else
        CYAN='' PURPLE='' ORANGE='' FIELD='' BOLD='' ITALIC='' RESET=''
    fi
}
setup_colors

# ── Output helpers ──────────────────────────────────────────────────

info()  { printf "${CYAN}%s${RESET}\n" "$*"; }
error() { printf "${ORANGE}%s${RESET}\n" "$*" >&2; exit 1; }

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

fetch_latest_version() {
    local url="https://api.github.com/repos/${REPO}/releases/latest"
    local response

    if command -v curl >/dev/null 2>&1; then
        response=$(curl -fsSL "$url") || error "Failed to fetch latest release info from GitHub."
    elif command -v wget >/dev/null 2>&1; then
        response=$(wget -qO- "$url") || error "Failed to fetch latest release info from GitHub."
    else
        error "Either curl or wget is required."
    fi

    VERSION=$(printf '%s' "$response" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
    [ -n "$VERSION" ] || error "Could not determine latest version."

    # Strip leading 'v' for the archive filename
    VERSION_NUM="${VERSION#v}"
}

# ── Download and extract ────────────────────────────────────────────

download() {
    local dest="$1"
    local url="$2"
    local archive="$3"
    local tmpdir

    tmpdir=$(mktemp -d) || error "Failed to create temporary directory."
    trap 'rm -rf "$tmpdir"' EXIT

    info "Downloading ${archive}..."
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${tmpdir}/${archive}" "$url" || error "Download failed: ${url}"
    else
        wget -qO "${tmpdir}/${archive}" "$url" || error "Download failed: ${url}"
    fi

    info "Extracting..."
    tar -xzf "${tmpdir}/${archive}" -C "$tmpdir" || error "Extraction failed."

    # goreleaser wraps in a directory
    local binary
    binary=$(find "$tmpdir" -name vibepit -type f | head -1)
    [ -n "$binary" ] || error "Could not find vibepit binary in archive."

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

    local archive="vibepit-${VERSION_NUM}-${PLATFORM_OS}-${PLATFORM_ARCH}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"

    echo "  ${BOLD}URL${RESET}  ${url}"
    echo "  ${BOLD}To${RESET}   ${dest}/vibepit"
    echo

    if $INTERACTIVE; then
        printf "Proceed? [Y/n] "
        read -r reply
        case "$reply" in
            ""|[Yy]|[Yy]es) ;;
            *)
                info "Download cancelled."
                exit 0
                ;;
        esac
        echo
    fi

    download "$dest" "$url" "$archive"

    echo
    info "Downloaded vibepit ${VERSION} to ${dest}/vibepit"
    info "Move it to a directory in your PATH to use it, for example:"
    echo
    printf "  ${BOLD}sudo mv %s/vibepit /usr/local/bin/${RESET}\n" "$dest"
    echo
}

main "$@"
