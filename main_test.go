package main

// Unit tests, run them in docker: docker compose run --rm test
// The end-to-end tests (the real binary with the real rsync) are in tests/

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// options parses the arguments like the command line does
func options(t *testing.T, args ...string) *Options {
	t.Helper()
	opt, err := getArguments(args)
	if err != nil {
		t.Fatalf("getArguments(%v): %v", args, err)
	}
	fixPaths(opt)
	return opt
}

// tail returns the last n items
func tail(list []string, n int) []string {
	return list[len(list)-n:]
}

func TestRsyncCommandLocal(t *testing.T) {
	command := rsyncCommand(options(t, "-s", "/src", "-d", "/dst", "-r", "/bin/rsync", "--nolog"))
	if command[0] != "/bin/rsync" {
		t.Errorf("rsync path: %v", command)
	}
	if !slices.Equal(tail(command, 2), []string{"/src/", "/dst/"}) {
		t.Errorf("local: %v", command)
	}
	if slices.Contains(command, "--append") {
		t.Errorf("--append without --rsync_append: %v", command)
	}
}

func TestRsyncCommandSSH(t *testing.T) {
	command := rsyncCommand(options(t, "-s", "/src", "-d", "/dst", "--nolog",
		"--host", "backup.example.com", "--port", "2222"))
	want := []string{"-e", "ssh -p 2222", "/src/", "backup.example.com:/dst/"}
	if !slices.Equal(tail(command, 4), want) {
		t.Errorf("ssh: %v", command)
	}
}

func TestRsyncCommandDaemon(t *testing.T) {
	command := rsyncCommand(options(t, "-s", "/src", "--nolog",
		"--rsync_daemon", "rsync.example.com::data",
		"--rsync_pass_file", "/etc/rsyncd.secrets"))
	want := []string{"--password-file=/etc/rsyncd.secrets", "/src/", "rsync.example.com::data"}
	if !slices.Equal(tail(command, 3), want) {
		t.Errorf("rsync daemon: %v", command)
	}
}

func TestRsyncCommandOptions(t *testing.T) {
	command := rsyncCommand(options(t, "--rsync_append", "--max_size", "100m",
		"--min_size", "10k", "--exclude_pattern", "var/tmp", "-l", "/var/log/rsync.log"))
	for _, arg := range []string{"--append", "--max-size=100m", "--min-size=10k",
		"--exclude=var/tmp", "--log-file=/var/log/rsync.log"} {
		if !slices.Contains(command, arg) {
			t.Errorf("%s is missing: %v", arg, command)
		}
	}
}

func TestRsyncCommandV2HasNoLogFile(t *testing.T) {
	command := rsyncCommand(options(t, "-l", "/tmp/x.log", "--rsync_v2"))
	for _, arg := range command {
		if len(arg) > 10 && arg[:10] == "--log-file" {
			t.Errorf("--rsync_v2 with --log-file: %v", command)
		}
	}
}

func TestRsyncCommandSpacesAreSafe(t *testing.T) {
	// no shell, so a path with spaces (or ; rm -rf) is just a path
	command := rsyncCommand(options(t, "-s", "/my data; rm -rf x", "-d", "/back up"))
	if !slices.Equal(tail(command, 2), []string{"/my data; rm -rf x/", "/back up/"}) {
		t.Errorf("spaces: %v", command)
	}
}

func TestBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-k", "restart"},
		{"--port", "abc"},
		{"extra"},
		{"--no_such_option"},
	} {
		if _, err := getArguments(args); err == nil {
			t.Errorf("%v must be an error", args)
		}
	}
}

// configErrors runs checkConfig with a good source, destination and pidfile
func configErrors(t *testing.T, tmp string, args ...string) []string {
	t.Helper()
	rsync, err := os.Executable() // any executable file is fine here
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"-s", filepath.Join(tmp, "src"), "-d", filepath.Join(tmp, "dst"),
		"-r", rsync, "-p", filepath.Join(tmp, "pid")}
	return checkConfig(options(t, append(base, args...)...))
}

func newTmp(t *testing.T) string {
	tmp := t.TempDir()
	os.Mkdir(filepath.Join(tmp, "src"), 0o755)
	os.Mkdir(filepath.Join(tmp, "dst"), 0o755)
	return tmp
}

func TestCheckConfig(t *testing.T) {
	tmp := newTmp(t)
	secrets := filepath.Join(tmp, "secrets")
	os.WriteFile(secrets, []byte("x"), 0o600)

	tests := []struct {
		name   string
		args   []string
		errors int
	}{
		{"good", nil, 0},
		{"missing source", []string{"-s", "/does/not/exist"}, 1},
		{"missing destination", []string{"-d", "/does/not/exist"}, 1},
		{"remote destination is not checked", []string{"-d", "/remote", "--host", "srv"}, 0},
		{"bad port", []string{"--port", "70000"}, 1},
		{"negative delay", []string{"--delay", "-1"}, 1},
		{"rsync daemon needs pass file", []string{"--rsync_daemon", "srv::data"}, 1},
		{"rsync daemon", []string{"--rsync_daemon", "srv::data", "--rsync_pass_file", secrets}, 0},
		{"rsync daemon with user", []string{"--rsync_daemon", "user@srv::data/sub", "--rsync_pass_file", secrets}, 0},
		{"bad rsync daemon", []string{"--rsync_daemon", "srv:data", "--rsync_pass_file", secrets}, 1},
	}
	for _, test := range tests {
		errors := configErrors(t, tmp, test.args...)
		if len(errors) != test.errors {
			t.Errorf("%s: want %d errors, got %v", test.name, test.errors, errors)
		}
	}
}

func TestRunningInstance(t *testing.T) {
	tmp := newTmp(t)
	// a process with pirsyncd in its command line
	sleeper := exec.Command("sh", "-c", "sleep 30", "pirsyncd")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Wait()
	defer sleeper.Process.Kill()

	os.WriteFile(filepath.Join(tmp, "pid"), []byte(strconv.Itoa(sleeper.Process.Pid)), 0o644)
	if errors := configErrors(t, tmp); len(errors) != 1 {
		t.Errorf("running pirsyncd: want 1 error, got %v", errors)
	}
}

func TestPidOfAnotherProgramIsOK(t *testing.T) {
	tmp := newTmp(t)
	// the pid was reused by another program, not pirsyncd
	// (not this test: the test binary is called pirsyncd.test)
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Wait()
	defer sleeper.Process.Kill()

	os.WriteFile(filepath.Join(tmp, "pid"), []byte(strconv.Itoa(sleeper.Process.Pid)), 0o644)
	if errors := configErrors(t, tmp); len(errors) != 0 {
		t.Errorf("another program: want no errors, got %v", errors)
	}
}

func TestStalePidfileIsOK(t *testing.T) {
	tmp := newTmp(t)
	os.WriteFile(filepath.Join(tmp, "pid"), []byte("999999999"), 0o644)
	if errors := configErrors(t, tmp); len(errors) != 0 {
		t.Errorf("stale pidfile: want no errors, got %v", errors)
	}
}

func TestExclude(t *testing.T) {
	opt := options(t, "-s", "/src", "--exclude_pattern", "var/tmp")
	for path, want := range map[string]bool{
		"/src/var/tmp":   true,
		"/src/var/tmp/a": true,
		"/src/var":       false,
	} {
		if isExcluded(path, opt) != want {
			t.Errorf("isExcluded(%s) must be %v", path, want)
		}
	}
	if isExcluded("/src/var/tmp", options(t, "-s", "/src")) {
		t.Error("no --exclude_pattern, nothing is excluded")
	}
}
