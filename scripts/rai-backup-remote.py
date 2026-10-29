#!/usr/bin/env python3
"""Sent over authenticated SSH; remote needs only Python 3 + POSIX file locks.

The JSON manifest is published last. It is the commit marker for a dump/SHA
pair; partial files and unrelated files never enter retention.
"""
import base64
import contextlib
import fcntl
import hashlib
import json
import os
import re
import stat
import sys
import time

FORMAT = "RAI-monthly-pg-custom/v1"
MARKER = b"RAI monthly backup directory v1\n"
NAME = re.compile(r"rai-\d{4}-\d{2}-\d{2}-\d{6}-[0-9a-f]{32}\.dump\Z")
NOFOLLOW = os.O_NOFOLLOW


def require(condition, code):
    if not condition:
        raise ValueError(code)


def directory(path, create):
    require(path.startswith("/") and path != "/", "invalid_remote_directory")
    parts = path[1:].split("/")
    require(all(p not in ("", ".", "..") for p in parts), "invalid_remote_directory")
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for index, part in enumerate(parts):
            if create and index == len(parts) - 1:
                try:
                    os.mkdir(part, 0o700, dir_fd=fd)
                except FileExistsError:
                    pass
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        info = os.fstat(fd)
        require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700,
                "remote_directory_must_be_owned_and_0700")
        return fd
    except BaseException:
        os.close(fd)
        raise


@contextlib.contextmanager
def regular(fd, name, flags=os.O_RDONLY):
    handle = os.open(name, flags | NOFOLLOW, 0o600, dir_fd=fd)
    try:
        info = os.fstat(handle)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and info.st_nlink == 1
                and stat.S_IMODE(info.st_mode) == 0o600, "unsafe_managed_file")
        with os.fdopen(handle, "wb" if flags & os.O_WRONLY else "rb", closefd=False) as file:
            yield file
    finally:
        os.close(handle)


def write_new(fd, name, data):
    with regular(fd, name, os.O_WRONLY | os.O_CREAT | os.O_EXCL) as file:
        file.write(data)
        file.flush()
        os.fsync(file.fileno())


def digest(file):
    h = hashlib.sha256()
    size = 0
    for block in iter(lambda: file.read(1024 * 1024), b""):
        size += len(block)
        h.update(block)
    return size, h.hexdigest()


def inventory(fd):
    valid, invalid = [], []
    for entry in sorted(os.listdir(fd)):
        if not entry.endswith(".dump.json") or not NAME.fullmatch(entry[:-5]):
            continue
        name = entry[:-5]
        try:
            with regular(fd, entry) as file:
                m = json.loads(file.read(8193))
            require(m["format"] == FORMAT and m["file"] == name and m["bytes"] > 0
                    and isinstance(m["committed_at_ns"], int), "invalid_manifest")
            with regular(fd, name + ".sha256") as file:
                require(file.read(1024) == (m["sha256"] + "  " + name + "\n").encode(), "invalid_sha_file")
            with regular(fd, name) as file:
                require(digest(file) == (m["bytes"], m["sha256"]), "integrity_mismatch")
            valid.append(m)
        except (OSError, ValueError, KeyError, TypeError):
            invalid.append(name)
    return sorted(valid, key=lambda m: (m["committed_at_ns"], m["file"])), invalid


def upload(fd, m):
    name = m["file"]
    require(NAME.fullmatch(name) and m["format"] == FORMAT, "invalid_backup_name")
    require(isinstance(m["bytes"], int) and m["bytes"] > 0
            and re.fullmatch(r"[0-9a-f]{64}", m["sha256"]), "invalid_backup_metadata")
    # Refuse even an orphan with this identity; never overwrite any prior file.
    names = [name, name + ".sha256", name + ".json"]
    for item in names + [n + ".partial" for n in names]:
        try:
            os.stat(item, dir_fd=fd, follow_symlinks=False)
        except FileNotFoundError:
            continue
        raise ValueError("backup_identity_exists")
    created = []
    published = False
    try:
        h = hashlib.sha256()
        size = 0
        with regular(fd, name + ".partial", os.O_WRONLY | os.O_CREAT | os.O_EXCL) as file:
            created.append(name + ".partial")
            for block in iter(lambda: sys.stdin.buffer.read(1024 * 1024), b""):
                size += len(block)
                require(size <= m["bytes"], "upload_size_mismatch")
                file.write(block)
                h.update(block)
            file.flush()
            os.fsync(file.fileno())
        require((size, h.hexdigest()) == (m["bytes"], m["sha256"]), "upload_integrity_mismatch")
        # Re-read the actual remote file before publication, not just stdin.
        with regular(fd, name + ".partial") as file:
            require(digest(file) == (m["bytes"], m["sha256"]), "remote_integrity_mismatch")
        old, invalid = inventory(fd)
        m["committed_at_ns"] = max(time.time_ns(), max((x["committed_at_ns"] for x in old), default=0) + 1)
        for suffix, data in [(".sha256", (m["sha256"] + "  " + name + "\n").encode()),
                             (".json", (json.dumps(m, sort_keys=True) + "\n").encode())]:
            item = name + suffix + ".partial"
            with regular(fd, item, os.O_WRONLY | os.O_CREAT | os.O_EXCL) as file:
                created.append(item)
                file.write(data)
                file.flush()
                os.fsync(file.fileno())
        for item in names:
            # Same filesystem and locked directory. Manifest is the last rename.
            os.rename(item + ".partial", item, src_dir_fd=fd, dst_dir_fd=fd)
            created.remove(item + ".partial")
            created.append(item)
            os.fsync(fd)
        published = True
        # Only verified successes made by this tool, oldest first. At least the
        # newest three (including this upload) remain even if unlink fails.
        removed = []
        for previous in (old + [m])[:-3]:
            candidate = previous["file"]
            for item in [candidate + ".json", candidate + ".sha256", candidate]:
                os.unlink(item, dir_fd=fd)
                os.fsync(fd)
            removed.append(candidate)
        return {"status": "success", "backup": m, "removed": removed, "invalid": invalid}
    finally:
        if not published:
            for item in created:
                try:
                    os.unlink(item, dir_fd=fd)
                except FileNotFoundError:
                    pass
            os.fsync(fd)


def main():
    os.umask(0o077)
    config = json.loads(base64.b64decode(sys.argv[1], validate=True))
    action = config["action"]
    require(action in ("backup", "check", "list"), "invalid_action")
    fd = directory(config["directory"], create=action == "backup")
    try:
        with regular(fd, ".rai-monthly.lock", os.O_RDWR | os.O_CREAT) as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                with regular(fd, ".rai-monthly-backup-v1") as marker:
                    require(marker.read(1024) == MARKER, "unmanaged_remote_directory")
            except FileNotFoundError:
                require(action == "backup" and set(os.listdir(fd)) == {".rai-monthly.lock"},
                        "refuse_to_adopt_nonempty_directory")
                write_new(fd, ".rai-monthly-backup-v1", MARKER)
                os.fsync(fd)
            if action == "backup":
                result = upload(fd, config["metadata"])
            else:
                valid, invalid = inventory(fd)
                result = {"status": "success" if valid and not invalid else "check_failed",
                          "backups": valid, "invalid": invalid}
            print(json.dumps(result, sort_keys=True))
            return 0 if result["status"] == "success" else 1
    finally:
        os.close(fd)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except BaseException as error:
        if isinstance(error, SystemExit):
            raise
        # No paths, credentials, payloads or raw remote diagnostics in logs.
        print("MONTHLY_REMOTE_FAILED=" + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
