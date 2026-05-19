#!/usr/bin/env python3
"""Import the Git history of one text file by replaying it through sfvc."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


VERSION_RE = re.compile(r"\bversion (\d+)\b")


Commit = str
Env = dict[str, str]


def run(
    cmd: list[str],
    *,
    cwd: Path | None = None,
    env: Env | None = None,
    text: bool = True,
    check: bool = True,
) -> subprocess.CompletedProcess[str | bytes]:
    try:
        proc = subprocess.run(
            cmd,
            cwd=cwd,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=text,
            encoding="utf-8" if text else None,
            errors="replace" if text else None,
            check=False,
        )
    except OSError as err:
        raise SystemExit(f"cannot run {cmd[0]}: {err}") from err
    if check and proc.returncode != 0:
        stderr = proc.stderr if text else proc.stderr.decode("utf-8", "replace")
        raise SystemExit(stderr.strip() or f"{cmd[0]} failed")
    return proc


def git(
    root: Path, *args: str, text: bool = True, check: bool = True
) -> subprocess.CompletedProcess[str | bytes]:
    return run(["git", *args], cwd=root, text=text, check=check)


def sfvc(
    sfvc_bin: str, args: list[str], *, env: Env | None = None
) -> subprocess.CompletedProcess[str]:
    return run([sfvc_bin, *args], env=env)


def repo_root(path: Path) -> Path:
    proc = run(["git", "rev-parse", "--show-toplevel"], cwd=path)
    return Path(proc.stdout.strip()).resolve()


def git_path(root: Path, path: Path) -> str:
    resolved = path.resolve(strict=False)
    try:
        return resolved.relative_to(root).as_posix()
    except ValueError:
        raise SystemExit(f"{path} is outside git repository {root}")


def rev_parse(root: Path, rev: str) -> Commit:
    return git(root, "rev-parse", "--verify", rev).stdout.strip()


def rev_list(root: Path, rev: str) -> list[tuple[Commit, list[Commit]]]:
    out = git(root, "rev-list", "--topo-order", "--reverse", "--parents", rev).stdout
    commits: list[tuple[Commit, list[Commit]]] = []
    for line in out.splitlines():
        parts = line.split()
        if parts:
            commits.append((parts[0], parts[1:]))
    return commits


def blob_at(root: Path, commit: Commit, path: str) -> bytes | None:
    spec = f"{commit}:{path}"
    exists = git(root, "cat-file", "-e", spec, check=False)
    if exists.returncode != 0:
        return None
    return git(root, "show", "--no-ext-diff", spec, text=False).stdout


def commit_metadata(root: Path, commit: Commit) -> tuple[str, str, str]:
    raw = git(root, "show", "-s", "--format=%an <%ae>%n%aI%n%B", commit).stdout
    author, date, *rest = raw.split("\n", 2)
    message = rest[0].rstrip("\n") if rest else ""
    return author, date, message


def sfvc_env(author: str, date: str, base_env: Env) -> Env:
    base_env["SFVC_AUTHOR"] = author
    base_env["SFVC_DATE"] = date
    return base_env


def parse_version(output: str) -> int:
    match = VERSION_RE.search(output)
    if not match:
        raise SystemExit(f"cannot parse sfvc version from output: {output!r}")
    return int(match.group(1))


def write_file(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def init_version(
    sfvc_bin: str, file: Path, commit: Commit, root: Path, content: bytes, env: Env
) -> int:
    author, date, message = commit_metadata(root, commit)
    write_file(file, content)
    proc = sfvc(sfvc_bin, ["init", str(file), "-m", message], env=sfvc_env(author, date, env))
    return parse_version(proc.stdout)


def commit_version(
    sfvc_bin: str,
    file: Path,
    commit: Commit,
    root: Path,
    parent_version: int,
    content: bytes,
    env: Env,
) -> int:
    author, date, message = commit_metadata(root, commit)
    sfvc(sfvc_bin, ["checkout", str(file), str(parent_version)])
    write_file(file, content)
    proc = sfvc(sfvc_bin, ["commit", str(file), "-m", message], env=sfvc_env(author, date, env))
    if "no changes" in proc.stdout:
        return parent_version
    return parse_version(proc.stdout)


def first_existing_parent(parent_versions: list[int | None]) -> int | None:
    return next((v for v in parent_versions if v is not None), None)


def warn_linearized_merge(commit: Commit, parent_versions: list[int | None]) -> None:
    relevant = sorted({v for v in parent_versions if v is not None})
    if len(relevant) > 1:
        short = commit[:12]
        versions = ", ".join(f"v{v}" for v in relevant)
        print(
            f"warning: merge commit {short} has multiple sfvc parents ({versions}); "
            "recording it through the first imported parent",
            file=sys.stderr,
        )


def replay_history(
    root: Path, rel_path: str, temp_file: Path, sfvc_bin: str, rev: str
) -> int:
    head_commit = rev_parse(root, rev)
    version_by_commit: dict[Commit, int | None] = {}
    initialized = False
    last_version: int | None = None
    base_env = os.environ.copy()

    for commit, parents in rev_list(root, head_commit):
        content = blob_at(root, commit, rel_path)
        parent_versions = [version_by_commit.get(parent) for parent in parents]
        parent_version = first_existing_parent(parent_versions)

        if content is None and parent_version is None:
            version_by_commit[commit] = None
            continue

        if not initialized:
            if content is None:
                version_by_commit[commit] = None
                continue
            version_by_commit[commit] = init_version(
                sfvc_bin, temp_file, commit, root, content, base_env,
            )
            last_version = version_by_commit[commit]
            initialized = True
            continue

        if content is None:
            # File deleted in this commit; carry forward the parent version.
            version_by_commit[commit] = parent_version
            continue

        if parent_version is None:
            parent_version = last_version
            print(
                f"warning: commit {commit[:12]} has no imported parent; "
                f"recording it on top of v{parent_version}",
                file=sys.stderr,
            )
        if len(parents) > 1:
            warn_linearized_merge(commit, parent_versions)
        version_by_commit[commit] = commit_version(
            sfvc_bin, temp_file, commit, root, parent_version, content, base_env,
        )
        last_version = version_by_commit[commit]

    head_version = version_by_commit.get(head_commit)
    if head_version is None:
        raise SystemExit(f"{rel_path} has no file history at {rev}")
    sfvc(sfvc_bin, ["checkout", str(temp_file), str(head_version)])
    return head_version


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Import one Git-managed text file into sfvc by replaying commits."
    )
    parser.add_argument("file", type=Path)
    parser.add_argument("--rev", default="HEAD", help="revision to import, default: HEAD")
    parser.add_argument("--sfvc", default="sfvc", help="sfvc binary to execute")
    parser.add_argument("--output", type=Path, help="weave path to write")
    parser.add_argument("--force", action="store_true", help="overwrite an existing weave")
    parser.add_argument("--stdout", action="store_true", help="print the generated weave JSON")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    root = repo_root(args.file.parent)
    rel_path = git_path(root, args.file)
    output: Path = args.output or args.file.parent / ".sfvc" / f"{args.file.name}.weave"

    if not args.stdout and output.exists() and not args.force:
        raise SystemExit(f"{output} already exists; pass --force to overwrite it")

    with tempfile.TemporaryDirectory(prefix="sfvc-git-import-") as tmp:
        temp_file = Path(tmp) / args.file.name
        replay_history(root, rel_path, temp_file, args.sfvc, args.rev)
        temp_weave = temp_file.parent / ".sfvc" / f"{temp_file.name}.weave"

        if args.stdout:
            sys.stdout.write(temp_weave.read_text(encoding="utf-8"))
            return 0

        output.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(temp_weave, output)
        print(f"wrote {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
