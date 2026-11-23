package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// rsync arguments, always used
var rsyncArgs = []string{"-azqx", "--delete-after", "--partial", "--safe-links",
	"--timeout=600"}

// If rsync fails, try again after retryDelay
const retryDelay = 30 * time.Second

// rsyncCommand builds the rsync command (as a list, no shell)
func rsyncCommand(opt *Options) []string {
	// Trailing slash (/): sync the content of the source directory
	source := strings.TrimRight(opt.SourcePath, "/") + "/"
	command := []string{opt.RsyncPath}
	command = append(command, rsyncArgs...)
	if opt.ExcludePat != "" {
		command = append(command, "--exclude="+opt.ExcludePat)
	}
	if opt.RsyncAppend {
		command = append(command, "--append")
	}
	if opt.MaxSize != "" {
		command = append(command, "--max-size="+opt.MaxSize)
	}
	if opt.MinSize != "" {
		command = append(command, "--min-size="+opt.MinSize)
	}
	// rsync version 2 does not support --log-file
	if opt.RsyncLog != "" && !opt.RsyncV2 {
		command = append(command, "--log-file="+opt.RsyncLog)
	}

	if opt.DestServer != "" {
		// rsync over ssh
		command = append(command, "-e", "ssh -p "+strconv.Itoa(opt.DestPort),
			source, opt.DestServer+":"+opt.DestPath)
	} else if opt.RsyncDaemon != "" {
		// rsync against an rsync server (rsync --daemon)
		command = append(command, "--password-file="+opt.RsyncPassFile,
			source, opt.RsyncDaemon)
	} else {
		// local rsync
		command = append(command, source, opt.DestPath)
	}
	return command
}

// mirror is the part that runs rsync. Returns true on success
func mirror(command []string) bool {
	logDebug("rsync: %s", strings.Join(command, " "))
	start := time.Now()
	var stderr bytes.Buffer
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	seconds := time.Since(start).Seconds()

	// rsync exit code 24 means "some files vanished before they could be
	// transferred", this is normal when files are changing all the time.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 24 {
		err = nil
	}
	if err == nil {
		logInfo("rsync done in %.2f seconds", seconds)
		return true
	}
	if errors.As(err, &exitErr) {
		logError("rsync failed with exit code %d: %s", exitErr.ExitCode(),
			strings.TrimSpace(stderr.String()))
	} else {
		logError("Can not run rsync: %v", err)
	}
	return false
}

// readEvents reads the inotify events for ever and sends them to the main loop
func readEvents(in *Inotify, eventsChan chan<- []Event) {
	for {
		events, err := in.ReadEvents()
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			logError("Can not read inotify events: %v", err)
			close(eventsChan)
			return
		}
		eventsChan <- events
	}
}

// watchLoop is the main loop: wait for events and run rsync when it is time.
// Returns the exit code.
func watchLoop(in *Inotify, command []string, opt *Options) int {
	pending := true          // do we need to run rsync? (yes, the first time)
	var firstEvent time.Time // time of the first event, since the last rsync
	var lastEvent time.Time  // time of the last event
	var lastSync time.Time   // time of the last good rsync
	var retryAt time.Time    // do not run rsync before that time (after an error)

	delay := seconds(opt.Delay)
	maxDelay := seconds(opt.MaxDelay)
	interval := seconds(opt.Interval)

	// How often we check the timers: every second, or faster for a short --delay
	tick := min(time.Second, max(100*time.Millisecond, delay))
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	// SIGTERM or SIGINT (ctrl + c): stop
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	// A goroutine reads the inotify events, the main loop gets them here
	eventsChan := make(chan []Event, 100)
	go readEvents(in, eventsChan)

	running := true
	for running {
		now := time.Now()

		// Periodic full rsync, even without events
		if interval > 0 && now.Sub(lastSync) >= interval {
			pending = true
		}

		// Run rsync after a quiet moment, but do not wait for ever
		if pending && !now.Before(retryAt) {
			quiet := now.Sub(lastEvent) >= delay
			tooLong := now.Sub(firstEvent) >= maxDelay
			if quiet || tooLong {
				if mirror(command) {
					pending = false
					lastSync = time.Now()
				} else {
					retryAt = time.Now().Add(retryDelay)
				}
			}
		}

		// Wait for new events, the next tick or a stop signal
		select {
		case events, ok := <-eventsChan:
			if !ok {
				return 1
			}
			if !handleEvents(in, events, opt) {
				logError("Source directory %s is gone, exit", opt.SourcePath)
				return 1
			}
			if !pending {
				pending = true
				firstEvent = time.Now()
			}
			lastEvent = time.Now()
		case <-ticker.C:
		case sig := <-stop:
			logInfo("Got signal %v, stopping", sig)
			running = false
		}
	}

	// We are stopping, do not leave changes behind
	if pending {
		logInfo("Last rsync before exit")
		mirror(command)
	}
	return 0
}

// seconds converts a number of seconds (from the command line) to a Duration
func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}
