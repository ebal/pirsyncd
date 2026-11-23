# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

pirsyncd now follows Semantic Versioning. The planned next version is `2.0.0`.
The older, date based versions (up to `20110412`) are in
[docs/HISTORY](./docs/HISTORY).

## [Unreleased]

### Added
- Add `--delay` and `--max_delay`: rsync runs once the directory is quiet,
  and at least every `--max_delay` seconds while it stays busy.
- Add `--interval` for a periodic full rsync, even without events.
- Add `--verbose` to log every inotify event.
- Accept `user@host::module` in `--rsync_daemon`.
- Retry a failed rsync after 30 seconds and log the rsync error message.
- Run a last rsync on stop (`SIGTERM`, `SIGINT`, `-k stop`).
- Watch new and moved-in directories, and rescan the tree after an inotify
  queue overflow.
- Add a systemd unit in `contrib/pirsyncd.service`.
- Add tests and a linter that run in docker (`docker compose run --rm test`).

### Changed
- Require Python 3.9 or newer. pirsyncd uses only the standard library, it
  talks to the kernel inotify directly (no pyinotify).
- `-k stop` stops pirsyncd with `SIGTERM` (was `SIGKILL`) and waits for it.
- `-k` works with `-p` in any order.
- A stale pidfile (no running pirsyncd) no longer blocks the start.
- `--port` must be a number from 1 to 65535 (was silently 22).
- The `--host` name is no longer resolved at start, so pirsyncd also starts
  when the DNS is not ready yet.
- rsync exit code 24 (files vanished during the transfer) is not an error.
- rsync runs with `--timeout=600`, so a hung connection does not block
  pirsyncd for ever.
- pirsyncd exits with an error when the source directory is removed.
- Convert README, INSTALL and Changelog to `README.md` and `CHANGELOG.md`.

### Removed
- Remove Python 2 support and `pirsyncd.Py3k`.
- Remove the vendored `pyinotify.py` and `pyinotify3.py`.
- Remove the inode event counter (`INODES`), replaced by `--delay`.

### Fixed
- Fix changes that were never synced: the last events before a quiet time,
  and a second change to the same file.
- Fix a memory leak: the duplicate event list grew for ever.
- Fix the first rsync running before the watches were ready, which could
  miss changes made during the start.
- Fix error messages that crashed pirsyncd (`str + OSError`).

### Security
- Run rsync without a shell, so source or destination paths can not run
  shell commands.
- `-k stop` only stops a real pirsyncd: a stale pidfile with a pid that now
  belongs to another program no longer kills that program.

[unreleased]: https://github.com/ebal/pirsyncd/compare/e2fa36d...HEAD
