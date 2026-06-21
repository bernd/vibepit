#!/usr/bin/env python3
"""Generate release metadata JSON files from a GitHub release.

Reads VERSION and TIMESTAMP from environment variables, parses changelog YAML
and checksums.txt, then writes per-version metadata and updates the channel
index file.

Usage:
    generate-release-metadata.py                   Generate metadata (default)
    generate-release-metadata.py --render-changelog Render changelog to stdout

Expected environment variables:
    VERSION     - Tag name with v prefix (e.g. v0.2.0)
    TIMESTAMP   - ISO 8601 timestamp (only for metadata generation)

Expected files:
    /tmp/checksums.txt                  - SHA256 checksums from release assets (metadata only)
    docs/changelogs/{VERSION}.yml       - Optional changelog in YAML format
"""

import argparse
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
REPO_URL = "https://github.com/bernd/vibepit"


def format_entry(entry: dict) -> str:
    line = "- " + entry["msg"]
    refs = []
    if pr := entry.get("pr"):
        refs.append(f"[#{pr}]({REPO_URL}/pull/{pr})")
    if issue := entry.get("issue"):
        refs.append(f"[#{issue}]({REPO_URL}/issues/{issue})")
    if refs:
        line += " (" + ", ".join(refs) + ")"
    return line


def parse_changelog(version: str, as_markdown: bool = False) -> str:
    changelog_file = Path(f"docs/changelogs/{version}.yml")
    if not changelog_file.exists():
        return ""

    with open(changelog_file) as f:
        data = yaml.safe_load(f)

    header_prefix = "\n### " if as_markdown else "\n"

    lines = []
    for category in CHANGELOG_CATEGORIES:
        entries = data.get(category, [])
        if entries:
            lines.append(header_prefix + category.capitalize() + ":")
            for entry in entries:
                lines.append(format_entry(entry))
    return "\n".join(lines)


def build_changes(version: str) -> dict:
    changelog_file = Path(f"docs/changelogs/{version}.yml")
    if not changelog_file.exists():
        return {}

    with open(changelog_file) as f:
        data = yaml.safe_load(f)

    changes = {}
    for category in CHANGELOG_CATEGORIES:
        entries = data.get(category, [])
        if not entries:
            continue
        out = []
        for entry in entries:
            item = {"msg": entry["msg"]}
            if pr := entry.get("pr"):
                item["pr"] = str(pr)
            if issue := entry.get("issue"):
                item["issue"] = str(issue)
            out.append(item)
        changes[category] = out
    return changes


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


def write_version_metadata(bare_version: str, timestamp: str, changelog: str, changes: dict, assets: list[dict]):
    RELEASES_DIR.mkdir(parents=True, exist_ok=True)
    meta = {
        "version": bare_version,
        "timestamp": timestamp,
        "changelog": changelog,
        "changes": changes,
        "assets": assets,
    }
    path = RELEASES_DIR / f"{bare_version}.json"
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


def render_changelog_cmd():
    version = os.environ.get("VERSION")
    if not version:
        sys.exit("VERSION environment variable is required")

    bare_version = version.lstrip("v")
    changelog = parse_changelog(bare_version, True)
    if changelog:
        print(changelog)


def generate_metadata_cmd():
    version = os.environ.get("VERSION")
    timestamp = os.environ.get("TIMESTAMP")

    if not version or not timestamp:
        sys.exit("VERSION and TIMESTAMP environment variables are required")

    bare_version = version.lstrip("v")

    changelog = parse_changelog(bare_version)
    changes = build_changes(bare_version)
    checksums = parse_checksums("/tmp/checksums.txt")
    assets = build_assets(version, bare_version, checksums)

    write_version_metadata(bare_version, timestamp, changelog, changes, assets)
    update_channel_index(bare_version, timestamp)


def backfill_changes_cmd():
    for path in sorted(RELEASES_DIR.glob("*.json")):
        if path.name in ("stable.json", "prerelease.json"):
            continue
        with open(path) as f:
            meta = json.load(f)
        version = meta.get("version")
        if not version:
            continue
        # Assigning an existing key preserves its position, so re-running this
        # on already-backfilled files is idempotent; a fresh file gets changes
        # appended (field order is irrelevant to consumers).
        meta["changes"] = build_changes(version)
        with open(path, "w") as f:
            json.dump(meta, f, indent=2)
            f.write("\n")
        print(f"backfilled {path.name}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--render-changelog", action="store_true",
                        help="Render changelog to stdout and exit")
    parser.add_argument("--backfill-changes", action="store_true",
                        help="Add the structured changes field to existing version JSONs and exit")
    args = parser.parse_args()

    if args.render_changelog:
        render_changelog_cmd()
    elif args.backfill_changes:
        backfill_changes_cmd()
    else:
        generate_metadata_cmd()
