#!/usr/bin/env python3
import hashlib
import json
import pathlib
import sys


def main() -> None:
    if len(sys.argv) != 7:
        raise SystemExit("usage: generate-controller-manifest.py OUT CHANNEL VERSION BUILD COMMIT DATE")
    output = pathlib.Path(sys.argv[1])
    channel, version, build, commit, date = sys.argv[2:]
    artifacts = []
    artifact_version = "dev" if channel == "dev" else version
    for arch in ("amd64", "arm64"):
        path = output / f"oboard_controller_{artifact_version}_linux_{arch}.tar.gz"
        if not path.is_file():
            raise SystemExit(f"controller release is missing {path.name}")
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        artifacts.append({
            "name": path.name,
            "os": "linux",
            "arch": arch,
            "sha256": digest.hexdigest(),
            "size": path.stat().st_size,
        })
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
