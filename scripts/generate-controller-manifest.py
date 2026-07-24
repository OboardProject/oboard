#!/usr/bin/env python3
import hashlib
import json
import pathlib
import re
import sys


def main() -> None:
    if len(sys.argv) != 7:
        raise SystemExit("usage: generate-controller-manifest.py OUT CHANNEL VERSION BUILD COMMIT DATE")
    output = pathlib.Path(sys.argv[1])
    channel, version, build, commit, date = sys.argv[2:]
    artifacts = []
    pattern = re.compile(r"^oboard_controller_.+_linux_(amd64|arm64)\.tar\.gz$")
    for path in sorted(output.glob("oboard_controller_*_linux_*.tar.gz")):
        match = pattern.match(path.name)
        if not match:
            continue
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        artifacts.append({
            "name": path.name,
            "os": "linux",
            "arch": match.group(1),
            "sha256": digest.hexdigest(),
            "size": path.stat().st_size,
        })
    if {item["arch"] for item in artifacts} != {"amd64", "arm64"}:
        raise SystemExit("controller release requires linux/amd64 and linux/arm64 packages")
    manifest = {
        "schema": 1,
        "channel": channel,
        "version": version,
        "build": build,
        "commit": commit,
        "date": date,
        "artifacts": artifacts,
    }
    (output / "controller-release-manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=True, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
