// pirsyncd stands for: Python Inotify Rsync Daemon (now written in Go)
//
// This is a small daemon that watches a directory for kernel's inotify events
// and then executes an rsync command to synchronize two different directories
// (local or remote). This is a poor man's mirroring or an alternative (not so)
// real time data replication mechanism.
//
// It is one static binary and it needs only rsync. Nothing else.
//
// Copyright (c) Evaggelos Balaskas (2009 - 2026)
// Licensed under GNU General Public License, version 2
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Version of pirsyncd (the build can change it with -ldflags "-X main.version=...")
var version = "2.0.0"

// Default values
const (
	SourcePath = "/tmp/data/"
	DestPath   = "/tmp/data2/"
	RsyncLog   = "/tmp/.pirsyncd.log"
	DebugLog   = "/tmp/.pirsyncdebug"
	PidFile    = "/tmp/.pirsyncd.pid"
	DestPort   = 22
	// Run rsync when there are no new events for Delay seconds ...
	Delay = 1.0
	// ... but never wait more than MaxDelay seconds (busy directories)
	MaxDelay = 60.0
	// Full rsync every Interval seconds, even without events (0 is disabled)
	Interval = 0.0
)

// The background pirsyncd is the same program, started again with this
// environment variable (go programs can not fork)
const backgroundEnv = "PIRSYNCD_BACKGROUND"

// Options are the user arguments
type Options struct {
	SourcePath    string
	DestPath      string
	PidFile       string
	RsyncPath     string
	RsyncLog      string
	NoLog         bool
	Foreground    bool
	DebugLog      string
	NoDebug       bool
	Verbose       bool
	RsyncAppend   bool
	RsyncV2       bool
	DestServer    string
	DestPort      int
	RsyncDaemon   string
	RsyncPassFile string
	MaxSize       string
	MinSize       string
	ExcludePat    string
	Delay         float64
	MaxDelay      float64
	Interval      float64
	Examples      bool
	Version       bool
	Action        string
}

// Usage message for help
const usageMsg = `usage: pirsyncd [options]

This is a daemon that watches a directory for kernel's inotify events and then
executes an rsync command to synchronize two different directories (local or
remote). This is a poor man's mirroring or an alternative (not so) real time
data replication mechanism.

options:
  -h, --help              show this help message and exit
  -v, --version           Print version and exit
  -s, --source DIR        This is the source (watched) directory. (default: /tmp/data/)
  -d, --destination DIR   This is the destination (to sync) directory. (default: /tmp/data2/)
  -p, --pidfile FILE      Full path of file contains the PID of the running instance.
                          Use a different pidfile for every instance of pirsyncd.
                          (default: /tmp/.pirsyncd.pid)
  -r, --rsync FILE        Full path of rsync command. (default: rsync in PATH)
  -l, --log FILE          Full path of rsync log file. (default: /tmp/.pirsyncd.log)
  --nolog                 Disable rsync logging
  -f, --foreground        Dont put the pirsyncd in background (logs are also printed on screen)
  --debug FILE            Full path of pirsyncd log file. (default: /tmp/.pirsyncdebug)
  --nodebug               Disable the pirsyncd log file
  --verbose               Log every inotify event (can be a lot!)
  --rsync_append          Enable rsync append functionality
  --rsync_v2              Disable rsync version 3 functionality (eg. log-file)
  --host HOST             Enable rsync over ssh. You must define a destination server
                          with passwordless (ssh key) connection.
  --port PORT             Define the destination ssh port, only with --host (default: 22)
  --rsync_daemon HOST::MODULE
                          Enable rsync over native rsync protocol. Valid argument is of
                          form [user@]host::module. If --host is set, then
                          --rsync_daemon is disabled.
  --rsync_pass_file FILE  Name of password file to be used in combination with rsync
                          daemon mode (--rsync_daemon).
  --max_size SIZE         Exclude files larger than this size from synchronization.
  --min_size SIZE         Exclude files smaller than this size from synchronization.
  --exclude_pattern PATTERN
                          Exclude pattern from rsync and inotify watch.
  --delay SECONDS         Run rsync after DELAY seconds without new events. (default: 1)
  --max_delay SECONDS     Run rsync at least every MAX_DELAY seconds while events keep
                          coming. (default: 60)
  --interval SECONDS      Run a full rsync every INTERVAL seconds, even without events.
                          0 is disabled. (default: 0)
  --examples              Print a list of pirsyncd usage examples and exit
  -k stop|status          With -k stop you stop the pirsyncd.
                          With -k status you can check the status of pirsyncd.
`

// Example messages for help
const exampleMsg = `
Below are many pirsyncd examples that you can use.
./pirsyncd -h

./pirsyncd -v

./pirsyncd

./pirsyncd -s /tmp/data/ -d /tmp/data2/

./pirsyncd -r /usr/local/bin/rsync

./pirsyncd -l /tmp/pirsyncd.log

./pirsyncd --nolog

./pirsyncd --debug /tmp/pirsyncdebug

./pirsyncd --nodebug

./pirsyncd --verbose

./pirsyncd --rsync_append

./pirsyncd --rsync_v2

./pirsyncd -s /tmp/data/ -d /tmp/data2/ --host ssh.example.com

./pirsyncd --host ssh.example.com --port 2222

./pirsyncd --rsync_daemon=rsync.example.com::data --rsync_pass_file=/etc/rsyncd.secrets

./pirsyncd --max_size 100m

./pirsyncd --min_size 10k

./pirsyncd --exclude_pattern=var/tmp

./pirsyncd --delay 2 --max_delay 30

./pirsyncd --interval 3600

./pirsyncd -k stop

./pirsyncd -k status

./pirsyncd -f

./pirsyncd -p /tmp/.pirsyncd.pid2

./pirsyncd --nodebug --nolog -s /tmp/data3/ -d /tmp/data4/ -p /tmp/.pirsyncd.pid2

When you are using a different pidfile, use it also with -k stop/status
./pirsyncd -p /tmp/.pirsyncd.pid2 -k status

./pirsyncd -p /tmp/.pirsyncd.pid2 -k stop
`

// ---------------------------------------------------------------------------
// logging

// Before setupLogging() the messages go to the screen
var logger = log.New(os.Stderr, "", 0)
var verbose = false

func logLine(level string, format string, args ...any) {
	logger.Printf("%s pirsyncd[%d] %s %s",
		time.Now().Format("2006-01-02 15:04:05,000"), os.Getpid(), level,
		fmt.Sprintf(format, args...))
}

func logDebug(format string, args ...any) {
	if verbose {
		logLine("DEBUG", format, args...)
	}
}

func logInfo(format string, args ...any)    { logLine("INFO", format, args...) }
func logWarning(format string, args ...any) { logLine("WARNING", format, args...) }
func logError(format string, args ...any)   { logLine("ERROR", format, args...) }

// setupLogging logs to the debug log file and, in foreground, also on screen
func setupLogging(opt *Options) {
	var writers []io.Writer
	if opt.DebugLog != "" {
		file, err := os.OpenFile(opt.DebugLog,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			logError("Can not open the log file: %v", err)
		} else {
			writers = append(writers, file)
		}
	}
	if opt.Foreground {
		writers = append(writers, os.Stderr)
	}
	logger.SetOutput(io.MultiWriter(writers...))
	verbose = opt.Verbose
}

// ---------------------------------------------------------------------------
// user arguments

// getArguments reads the user arguments
func getArguments(args []string) (*Options, error) {
	opt := &Options{}
	flags := flag.NewFlagSet("pirsyncd", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(flags.Output(), usageMsg) }

	rsyncPath, err := exec.LookPath("rsync")
	if err != nil {
		rsyncPath = "/usr/bin/rsync"
	}

	// Options with a short and a long name use the same variable
	flags.BoolVar(&opt.Version, "v", false, "")
	flags.BoolVar(&opt.Version, "version", false, "")
	flags.StringVar(&opt.SourcePath, "s", SourcePath, "")
	flags.StringVar(&opt.SourcePath, "source", SourcePath, "")
	flags.StringVar(&opt.DestPath, "d", DestPath, "")
	flags.StringVar(&opt.DestPath, "destination", DestPath, "")
	flags.StringVar(&opt.PidFile, "p", PidFile, "")
	flags.StringVar(&opt.PidFile, "pidfile", PidFile, "")
	flags.StringVar(&opt.RsyncPath, "r", rsyncPath, "")
	flags.StringVar(&opt.RsyncPath, "rsync", rsyncPath, "")
	flags.StringVar(&opt.RsyncLog, "l", RsyncLog, "")
	flags.StringVar(&opt.RsyncLog, "log", RsyncLog, "")
	flags.BoolVar(&opt.NoLog, "nolog", false, "")
	flags.BoolVar(&opt.Foreground, "f", false, "")
	flags.BoolVar(&opt.Foreground, "foreground", false, "")
	flags.StringVar(&opt.DebugLog, "debug", DebugLog, "")
	flags.BoolVar(&opt.NoDebug, "nodebug", false, "")
	flags.BoolVar(&opt.Verbose, "verbose", false, "")
	flags.BoolVar(&opt.RsyncAppend, "rsync_append", false, "")
	flags.BoolVar(&opt.RsyncV2, "rsync_v2", false, "")
	flags.StringVar(&opt.DestServer, "host", "", "")
	flags.IntVar(&opt.DestPort, "port", DestPort, "")
	flags.StringVar(&opt.RsyncDaemon, "rsync_daemon", "", "")
	flags.StringVar(&opt.RsyncPassFile, "rsync_pass_file", "", "")
	flags.StringVar(&opt.MaxSize, "max_size", "", "")
	flags.StringVar(&opt.MinSize, "min_size", "", "")
	flags.StringVar(&opt.ExcludePat, "exclude_pattern", "", "")
	flags.Float64Var(&opt.Delay, "delay", Delay, "")
	flags.Float64Var(&opt.MaxDelay, "max_delay", MaxDelay, "")
	flags.Float64Var(&opt.Interval, "interval", Interval, "")
	flags.BoolVar(&opt.Examples, "examples", false, "")
	flags.StringVar(&opt.Action, "k", "", "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() > 0 {
		err := fmt.Errorf("unknown argument: %s", flags.Arg(0))
		fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if opt.Action != "" && opt.Action != "stop" && opt.Action != "status" {
		err := fmt.Errorf("-k: there isnt any action for: %s (stop or status)",
			opt.Action)
		fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if opt.NoLog {
		opt.RsyncLog = ""
	}
	if opt.NoDebug {
		opt.DebugLog = ""
	}
	return opt, nil
}

// fixPaths uses full paths, because the daemon runs from /
func fixPaths(opt *Options) {
	opt.SourcePath = absPath(opt.SourcePath)
	opt.PidFile = absPath(opt.PidFile)
	if opt.RsyncLog != "" {
		opt.RsyncLog = absPath(opt.RsyncLog)
	}
	if opt.DebugLog != "" {
		opt.DebugLog = absPath(opt.DebugLog)
	}
	if opt.RsyncPassFile != "" {
		opt.RsyncPassFile = absPath(opt.RsyncPassFile)
	}
	// the destination can be on a remote server, use it as it is
	if opt.DestServer == "" && opt.RsyncDaemon == "" {
		opt.DestPath = absPath(opt.DestPath)
	}
	if !strings.HasSuffix(opt.DestPath, "/") {
		opt.DestPath += "/"
	}
}

// absPath is the full path, without a trailing slash (/)
func absPath(path string) string {
	full, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return full
}

// Valid is something like this: [user@]rsync.example.com::data
var rsyncDaemonRegexp = regexp.MustCompile(`^([\w.-]+@)?[\w.-]+::[\w.-]+(/.*)?$`)

// checkConfig validates the configuration of pirsyncd, returns a list of errors
func checkConfig(opt *Options) []string {
	var errors []string
	pid := readPid(opt.PidFile)
	if isRunning(pid) {
		errors = append(errors, "There is a running pirsyncd instance already, PID: "+
			strconv.Itoa(pid)+" (pidfile: "+opt.PidFile+")")
	}
	if !isDir(opt.SourcePath) {
		errors = append(errors, "There isn't any (source directory): "+opt.SourcePath)
	}
	if !isExecutable(opt.RsyncPath) {
		errors = append(errors, "There isn't any rsync: "+opt.RsyncPath)
	}
	if opt.DestPort < 1 || opt.DestPort > 65535 {
		errors = append(errors, "ssh port must be between 1 and 65535: "+
			strconv.Itoa(opt.DestPort))
	}
	if opt.Delay < 0 || opt.MaxDelay < 0 || opt.Interval < 0 {
		errors = append(errors, "--delay, --max_delay and --interval can not be negative")
	}

	// Both RsyncDaemon & RsyncPassFile arguments must have values.
	if (opt.RsyncDaemon == "") != (opt.RsyncPassFile == "") {
		errors = append(errors, "--rsync_daemon works only together with --rsync_pass_file")
	}

	if opt.DestServer != "" {
		// rsync over ssh, the destination directory is on the remote server
	} else if opt.RsyncDaemon != "" {
		if !rsyncDaemonRegexp.MatchString(opt.RsyncDaemon) {
			errors = append(errors, "rsync_daemon: "+opt.RsyncDaemon+
				", isnt a valid argument. You must type something like this: "+
				"rsync.example.com::data")
		}
		if opt.RsyncPassFile != "" && !isFile(opt.RsyncPassFile) {
			errors = append(errors, "There isn't any : "+opt.RsyncPassFile)
		}
	} else if !isDir(opt.DestPath) {
		errors = append(errors, "There isn't any (destination directory): "+opt.DestPath)
	}
	return errors
}

// isFile is true if path is a regular file
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// isExecutable is true if path is a file that we can run
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// ---------------------------------------------------------------------------
// pidfile

// readPid returns the pid in the pidfile, or 0
func readPid(pidfile string) int {
	data, err := os.ReadFile(pidfile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// isRunning is true if there is a running pirsyncd with this pid.
// We check the command line, because after a crash or a reboot
// the same pid can belong to another program.
func isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	return strings.Contains(string(cmdline), "pirsyncd")
}

// removePidfile removes the pidfile, if it is still there
func removePidfile(pidfile string) {
	os.Remove(pidfile)
}

// myAction is for -k stop or -k status. Returns the exit code
func myAction(opt *Options) int {
	pid := readPid(opt.PidFile)
	if !isRunning(pid) {
		fmt.Println("There is no running pirsyncd instance (pidfile: " +
			opt.PidFile + ")")
		if pid != 0 {
			// stale pidfile from a crashed pirsyncd
			removePidfile(opt.PidFile)
		}
		return 1
	}

	if opt.Action == "status" {
		fmt.Println("There is a running instance of pirsyncd, with PID: " +
			strconv.Itoa(pid))
		return 0
	}

	// stop: ask nicely (SIGTERM), pirsyncd runs a last rsync and exits
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Println("Can not stop pirsyncd with PID: " + strconv.Itoa(pid) +
			": " + err.Error())
		return 1
	}
	for i := 0; i < 600; i++ {
		if !isRunning(pid) {
			fmt.Println("pirsyncd with PID: " + strconv.Itoa(pid) +
				" stopped successfully")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("pirsyncd with PID: " + strconv.Itoa(pid) + " is still running " +
		"(perhaps a long rsync), check again with: -k status")
	return 1
}

// ---------------------------------------------------------------------------
// the daemon

// startBackground starts pirsyncd again, as a background process
func startBackground() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), backgroundEnv+"=1")
	// a new session: not stopped when the terminal closes.
	// stdin, stdout and stderr are /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// pirsyncd starts watching the source directory and runs rsync on changes
func pirsyncd(opt *Options) int {
	background := os.Getenv(backgroundEnv) == "1"

	fixPaths(opt)
	if errors := checkConfig(opt); len(errors) > 0 {
		for _, line := range errors {
			fmt.Fprintln(os.Stderr, line)
		}
		fmt.Fprintln(os.Stderr, "Please check your configuration settings. Try: pirsyncd -h")
		return 1
	}

	if !opt.Foreground && !background {
		fmt.Println("pirsyncd is starting, watching " + opt.SourcePath)
		if err := startBackground(); err != nil {
			fmt.Fprintln(os.Stderr, "Can not start in background: "+err.Error())
			return 1
		}
		fmt.Println("pirsyncd runs in background, try: pirsyncd -p " +
			opt.PidFile + " -k status")
		return 0
	}

	if background {
		// do not keep any directory busy
		os.Chdir("/")
	}

	// remove a stale pidfile from a crashed pirsyncd
	removePidfile(opt.PidFile)

	command := rsyncCommand(opt)

	// First watch, then rsync, so we do not miss any change in between
	if opt.Foreground {
		fmt.Println("pirsyncd is starting, watching " + opt.SourcePath)
	}
	in, err := NewInotify()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Can not start inotify: "+err.Error())
		return 1
	}
	watchTree(in, opt.SourcePath, opt)
	if opt.Foreground {
		fmt.Println("pirsyncd runs in foreground (ctrl + c to stop the daemon)")
	}

	setupLogging(opt)

	err = os.WriteFile(opt.PidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	if err != nil {
		logError("Can not write the pidfile: %v", err)
		return 1
	}
	defer removePidfile(opt.PidFile)

	logInfo("pirsyncd %s started, %d directories watched", version, len(in.watches))
	code := watchLoop(in, command, opt)
	logInfo("pirsyncd stopped")
	return code
}

// run reads the user arguments and does what they ask. Returns the exit code
func run(args []string) int {
	opt, err := getArguments(args)
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		return 2
	}
	if opt.Version {
		fmt.Println("pirsyncd " + version)
		return 0
	}
	if opt.Examples {
		fmt.Print(exampleMsg)
		return 0
	}
	if opt.Action != "" {
		opt.PidFile = absPath(opt.PidFile)
		return myAction(opt)
	}
	return pirsyncd(opt)
}

func main() {
	os.Exit(run(os.Args[1:]))
}
