#!/usr/bin/env bash
# Builds ghostty-vt.wasm at a pinned ghostty commit with vibepit's patches,
# records its provenance in GHOSTTY_COMMIT, and translates it to Go with
# wasm2go (the go:generate line in internal/wasmvt/doc.go).
#
# Exit codes: 3 = a patch does not apply (the ghostty-wasm workflow reports
# this category separately), anything else nonzero = build failure.
set -euo pipefail

commit="${1:?usage: build-wasm.sh <ghostty-commit>}"
here="$(cd "$(dirname "$0")" && pwd)"
out="$here/internal/wasmvt"
src="${GHOSTTY_SRC:-${TMPDIR:-/tmp}/vibepit-ghostty-src}"

zig_version="$(zig version)"
case "$zig_version" in
  0.16.*) ;;
  *) echo "need Zig 0.16, found $zig_version" >&2; exit 1 ;;
esac

if [ ! -d "$src/.git" ]; then
  git clone --filter=blob:none https://github.com/ghostty-org/ghostty.git "$src"
fi
git -C "$src" fetch --quiet origin
# --force drops the patches applied by an earlier run.
git -C "$src" checkout --quiet --force "$commit"
full="$(git -C "$src" rev-parse HEAD)"

for p in "$here"/patches/*.patch; do
  if ! git -C "$src" apply --check "$p"; then
    echo "patch does not apply: $(basename "$p")" >&2
    exit 3
  fi
  git -C "$src" apply "$p"
done

# ghostty enables simd128 on wasm unless a CPU is given, and wasm2go can't
# translate SIMD instructions.
(cd "$src" && zig build -Demit-lib-vt -Dtarget=wasm32-freestanding -Dcpu=generic -Doptimize=ReleaseSmall)

cp "$src/zig-out/bin/ghostty-vt.wasm" "$out/ghostty-vt.wasm"
if command -v sha256sum >/dev/null; then
  sum="$(sha256sum "$out/ghostty-vt.wasm" | cut -d' ' -f1)"
else
  sum="$(shasum -a 256 "$out/ghostty-vt.wasm" | cut -d' ' -f1)"
fi
printf 'commit=%s\nzig=%s\nsha256=%s\n' "$full" "$zig_version" "$sum" > "$out/GHOSTTY_COMMIT"
(cd "$out" && go generate .)
echo "built ghostty-vt.wasm at $full ($sum) and regenerated ghostty_vt.go"
