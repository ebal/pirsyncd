# pirsyncd

pirsyncd stands for **P**ython **I**notify **Rsync** **D**aemon.

It watches a directory with the Linux kernel [inotify](https://man7.org/linux/man-pages/man7/inotify.7.html)
events and runs [rsync](https://rsync.samba.org/) when something changes, to
keep a second directory (local, over ssh, or on an rsync server) in sync.
It is a poor man's mirroring: a simple, near real time data replication
without a cluster file system.

pirsyncd is one small static binary written in Go (it was python, the name
stays). It needs only rsync on the machine, nothing else.

![CI](https://img.shields.io/github/actions/workflow/status/ebal/pirsyncd/ci.yml)
![License](https://img.shields.io/github/license/ebal/pirsyncd)

### How it works

1. pirsyncd watches the source directory and all of its sub directories.
2. It runs a first rsync at start, so the destination is in sync.
3. On every change it waits for a quiet moment (`--delay`, 1 second) and
   then runs **one** rsync for all the changes. When the directory never
   gets quiet, it still runs rsync at least every `--max_delay` (60) seconds.
4. If rsync fails, it tries again after 30 seconds. On stop, it runs a last
   rsync, so no change is left behind.

## Installation

### Requirements

- Linux (inotify), amd64 or arm64
- rsync (version 3 recommended, for `--log-file`)
- ssh, for rsync over ssh (`--host`)

### Download the binary

Every push builds static binaries on GitHub Actions. Open the latest
[ci workflow run](https://github.com/ebal/pirsyncd/actions/workflows/ci.yml)
and download `pirsyncd-linux-amd64` or `pirsyncd-linux-arm64` from
*Artifacts*. Then:

```bash
unzip pirsyncd-linux-amd64.zip
install -m 0755 pirsyncd /usr/local/bin/pirsyncd
pirsyncd -v
```

### From source

You need only docker, the build runs in the official golang image:

```bash
git clone https://github.com/ebal/pirsyncd.git
cd pirsyncd
docker compose run --rm build      # creates dist/pirsyncd
./dist/pirsyncd -h
```

For another cpu: `GOARCH=arm64 docker compose run --rm build`.

### systemd

A unit file is in [contrib/pirsyncd.service](./contrib/pirsyncd.service).
Edit the `ExecStart` line (source, destination, options) and then:

```bash
cp contrib/pirsyncd.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pirsyncd
journalctl -u pirsyncd -f
```

## Usage

Sync a local directory to another local directory, in foreground:

```bash
mkdir -p /tmp/data /tmp/data2
pirsyncd -f -s /tmp/data/ -d /tmp/data2/
```

```
pirsyncd is starting, watching /tmp/data
pirsyncd runs in foreground (ctrl + c to stop the daemon)
2026-11-23 16:00:00,000 pirsyncd[1234] INFO pirsyncd 2.0.0 started, 1 directories watched
2026-11-23 16:00:00,020 pirsyncd[1234] INFO rsync done in 0.02 seconds
```

Run it in background, check it and stop it:

```bash
pirsyncd -s /tmp/data/ -d /tmp/data2/
pirsyncd -k status
pirsyncd -k stop
```

```
There is a running instance of pirsyncd, with PID: 1234
pirsyncd with PID: 1234 stopped successfully
```

Sync over ssh (you need a passwordless ssh key for the remote server):

```bash
./pirsyncd -s /srv/data/ -d /backup/data/ --host backup.example.com --port 2222
```

Sync to an rsync server (`rsync --daemon`):

```bash
./pirsyncd -s /srv/data/ --rsync_daemon=user@rsync.example.com::data \
           --rsync_pass_file=/etc/rsyncd.secrets
```

More instances at the same time: use a different pidfile (and logs) for each
one, and the same pidfile with `-k`:

```bash
./pirsyncd -s /tmp/data3/ -d /tmp/data4/ -p /tmp/.pirsyncd.pid2 --nodebug --nolog
./pirsyncd -p /tmp/.pirsyncd.pid2 -k status
```

See all the options and more examples:

```bash
./pirsyncd -h
./pirsyncd --examples
```

### Main options

| Option | Default | What it does |
| --- | --- | --- |
| `-s DIR` | `/tmp/data/` | source (watched) directory |
| `-d DIR` | `/tmp/data2/` | destination directory |
| `-f` | | run in foreground, logs also on screen |
| `-p FILE` | `/tmp/.pirsyncd.pid` | pidfile |
| `-l FILE` / `--nolog` | `/tmp/.pirsyncd.log` | rsync log file |
| `--debug FILE` / `--nodebug` | `/tmp/.pirsyncdebug` | pirsyncd log file |
| `--verbose` | | log every inotify event |
| `--delay SECONDS` | `1` | run rsync after this quiet time |
| `--max_delay SECONDS` | `60` | run rsync at least this often while busy |
| `--interval SECONDS` | `0` (off) | full rsync every N seconds, even without events |
| `--exclude_pattern PATTERN` | | do not sync or watch this path |
| `--max_size` / `--min_size SIZE` | | skip big / small files |
| `--host HOST` `--port PORT` | | rsync over ssh |
| `--rsync_daemon` `--rsync_pass_file` | | rsync to an rsync server |
| `-k stop` / `-k status` | | stop or check the running pirsyncd |

### Signals

- `SIGTERM` or `SIGINT` (ctrl + c): run a last rsync (if there are changes)
  and exit. `-k stop` sends `SIGTERM`.

### Known issues and tips

- **Many directories**: pirsyncd needs one inotify watch per directory.
  If you see `No more inotify watches`, raise the limit:

  ```bash
  cat /proc/sys/fs/inotify/max_user_watches
  sysctl fs.inotify.max_user_watches=1048576
  ```

- **Event queue overflow**: when the kernel queue is full, events are lost.
  pirsyncd notices it, watches the tree again and runs rsync, so nothing is
  lost on the destination. You can raise the queue size:

  ```bash
  cat /proc/sys/fs/inotify/max_queued_events
  sysctl fs.inotify.max_queued_events=32768
  ```

- **Safety net**: use `--interval 3600` to run a full rsync every hour, even
  without events (for example after a missing watch).

## Development

Everything runs in docker, you do not need go, python or rsync on your
machine:

```bash
docker compose run --rm build   # static binary: dist/pirsyncd
docker compose run --rm test    # gofmt, go vet and go unit tests
docker compose run --rm e2e     # end-to-end tests of dist/pirsyncd with rsync
docker compose run --rm lint    # ruff linter for the python test file
```

The code is in four small files: [main.go](./main.go) (options, pidfile,
daemon), [inotify.go](./inotify.go) (watches), [rsync.go](./rsync.go)
(rsync and the main loop) and [main_test.go](./main_test.go) (unit tests).
Only the Go standard library is used.

The end-to-end tests in [tests/test_pirsyncd.py](./tests/test_pirsyncd.py)
run the real binary with the real rsync, using only the command line.

## Contributing

Pull requests are welcome. Please keep it simple (standard library only),
add a test for your change, and run the docker commands above before you
send it. Commit messages follow
[Conventional Commits](https://www.conventionalcommits.org/).

## Support

- [Issue Tracker](https://github.com/ebal/pirsyncd/issues)
- A FAQ page (mostly in greek): <https://ebalaskas.gr/wiki/pirsyncd>
- Blog: <https://balaskas.gr/blog/?page=pirsyncd>

## Authors and acknowledgment

Evaggelos Balaskas. Thanks to Jeff Templon, Jan Just Keijser, Bryan Seitz and
Fred Warren for their patches and ideas (see [docs/HISTORY](./docs/HISTORY)).

pirsyncd 1.x was based on [pyinotify](https://github.com/seb-m/pyinotify)
by Sébastien Martini.

## License

[GNU General Public License, version 2](./LICENSE)
