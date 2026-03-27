#!/usr/bin/env python3
"""Generate release metadata JSON files from a GitHub release.

Reads VERSION and TIMESTAMP from environment variables, parses changelog YAML
and checksums.txt, then writes per-version metadata and updates the channel
index file.

Expected environment variables:
    VERSION     - Tag name with v prefix (e.g. v0.2.0)
    TIMESTAMP   - ISO 8601 timestamp of the release

Expected files:
    /tmp/checksums.txt                  - SHA256 checksums from release assets
    docs/changelogs/{VERSION}.yml       - Optional changelog in YAML format
"""

import json
import os
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.exit("pyyaml is required: pip install pyyaml")

RELEASES_DIR = Path("docs/content/releases")

PLATFORM_MAP = {
    "linux-x86_64": ("linux", "amd64"),
    "linux-aarch64": ("linux", "arm64"),
    "darwin-x86_64": ("darwin", "amd64"),
    "darwin-aarch64": ("darwin", "arm64"),
}

CHANGELOG_CATEGORIES = ["added", "changed", "fixed", "deprecated", "removed", "security"]


def parse_changelog(version: str) -> str:
    changelog_file = Path(f"docs/changelogs/{version}.yml")
    if not changelog_file.exists():
        return ""

    with open(changelog_file) as f:
        data = yaml.safe_load(f)

    lines = []
    for category in CHANGELOG_CATEGORIES:
        entries = data.get(category, [])
        if entries:
            lines.append(category.capitalize() + ":")
            for entry in entries:
                lines.append("- " + entry["description"])
    return "\n".join(lines)


def parse_checksums(path: str) -> dict[str, str]:
    checksums = {}
    with open(path) as f:
        for line in f:
            parts = line.strip().split()
            if len(parts) == 2:
                checksums[parts[1]] = parts[0]
    return checksums


def build_assets(version: str, bare_version: str, checksums: dict[str, str]) -> list[dict]:
    base_url = f"https://github.com/bernd/vibepit/releases/download/{version}"
    assets = []
    for suffix, (os_name, arch) in PLATFORM_MAP.items():
        tarball = f"vibepit-{bare_version}-{suffix}.tar.gz"
        if tarball in checksums:
            assets.append({
                "os": os_name,
                "arch": arch,
                "url": f"{base_url}/{tarball}",
                "sha256": checksums[tarball],
                "cosign_bundle_url": f"{base_url}/{tarball}.bundle",
            })
    return assets


def write_version_metadata(bare_version: str, timestamp: str, changelog: str, assets: list[dict]):
    RELEASES_DIR.mkdir(parents=True, exist_ok=True)
    meta = {
        "version": bare_version,
        "timestamp": timestamp,
        "changelog": changelog,
        "assets": assets,
    }
    path = RELEASES_DIR / f"v{bare_version}.json"
    with open(path, "w") as f:
        json.dump(meta, f, indent=2)
        f.write("\n")


def update_channel_index(bare_version: str, timestamp: str):
    channel = "prerelease" if "-" in bare_version else "stable"
    channel_file = RELEASES_DIR / f"{channel}.json"

    try:
        with open(channel_file) as f:
            idx = json.load(f)
    except FileNotFoundError:
        idx = {"latest": "", "releases": []}

    idx["latest"] = bare_version
    idx["releases"].insert(0, {"version": bare_version, "timestamp": timestamp})

    with open(channel_file, "w") as f:
        json.dump(idx, f, indent=2)
        f.write("\n")


def main():
    version = os.environ.get("VERSION")
    timestamp = os.environ.get("TIMESTAMP")

    if not version or not timestamp:
        sys.exit("VERSION and TIMESTAMP environment variables are required")

    bare_version = version.lstrip("v")

    changelog = parse_changelog(version)
    checksums = parse_checksums("/tmp/checksums.txt")
    assets = build_assets(version, bare_version, checksums)

    write_version_metadata(bare_version, timestamp, changelog, assets)
    update_channel_index(bare_version, timestamp)


if __name__ == "__main__":
    main()
