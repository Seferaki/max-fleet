"""Check tracked or staged Git blobs without printing their contents."""

import argparse
import re
import subprocess
import sys


PATTERNS = {
    "private-key": re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "github-token": re.compile(rb"(?:ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})"),
    "aws-access-key": re.compile(rb"AKIA[0-9A-Z]{16}"),
    "authorization-value": re.compile(rb"(?i)Authorization\s*[:=]\s*Bearer\s+[A-Za-z0-9._-]{20,}"),
}
# data/ запрещён только в корне (как /data/ в .gitignore); services/data/ — код Python-сервиса.
FORBIDDEN_PATH = re.compile(r"(^|/)(?:\.env(?:\..+)?|secrets/|backups/)|^data/|\.(?:pem|key|p12|pfx|dump|sqlite)$")


def git(*args):
    return subprocess.check_output(["git", *args])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--staged", action="store_true", help="Проверить индекс перед commit")
    parser.add_argument("--tracked", action="store_true", help="Проверить все tracked-файлы для CI")
    args = parser.parse_args()
    if args.staged == args.tracked:
        parser.error("Выберите ровно один из --staged или --tracked")

    listing = (git("diff", "--cached", "--name-only", "--diff-filter=ACMRT", "-z")
               if args.staged else git("ls-files", "-z"))
    paths = [item.decode("utf-8") for item in listing.split(b"\0") if item]
    failures = []
    for path in paths:
        normalized = path.replace("\\", "/")
        if normalized != ".env.example" and FORBIDDEN_PATH.search(normalized):
            failures.append((path, "forbidden-path"))
            continue
        revision = ":" + path if args.staged else "HEAD:" + path
        try:
            content = git("show", revision)
        except subprocess.CalledProcessError:
            failures.append((path, "unreadable-blob"))
            continue
        for name, pattern in PATTERNS.items():
            if pattern.search(content):
                failures.append((path, name))

    for path, rule in failures:
        print(f"Проверка секретов: {path} ({rule})", file=sys.stderr)
    if failures:
        return 1
    print(f"Проверка секретов: {len(paths)} Git-файлов, совпадений нет")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
