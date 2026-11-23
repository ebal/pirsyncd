package main

// A tiny wrapper around the linux inotify system calls (see: man 7 inotify)

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The inotify events that pirsyncd watches
const watchMask = syscall.IN_MODIFY | syscall.IN_ATTRIB | syscall.IN_CLOSE_WRITE |
	syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO | syscall.IN_CREATE |
	syscall.IN_DELETE | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF |
	syscall.IN_ONLYDIR | syscall.IN_DONT_FOLLOW

// Event is one inotify event
type Event struct {
	Wd   int
	Mask uint32
	Name string
}

// Inotify keeps the inotify file descriptor and the watched directories
type Inotify struct {
	fd      int
	watches map[int]string // watch descriptor -> directory path
}

// NewInotify starts a new inotify instance
func NewInotify() (*Inotify, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &Inotify{fd: fd, watches: map[int]string{}}, nil
}

// AddWatch watches one directory (not recursive)
func (in *Inotify) AddWatch(path string) error {
	wd, err := syscall.InotifyAddWatch(in.fd, path, watchMask)
	if err != nil {
		return err
	}
	in.watches[wd] = path
	return nil
}

// ReadEvents waits for events and returns them
func (in *Inotify) ReadEvents() ([]Event, error) {
	buf := make([]byte, 64*1024)
	n, err := syscall.Read(in.fd, buf)
	if err != nil {
		return nil, err
	}
	var events []Event
	// struct inotify_event { int wd; uint32 mask, cookie, len; char name[]; }
	for offset := 0; offset+syscall.SizeofInotifyEvent <= n; {
		wd := int32(binary.NativeEndian.Uint32(buf[offset:]))
		mask := binary.NativeEndian.Uint32(buf[offset+4:])
		length := int(binary.NativeEndian.Uint32(buf[offset+12:]))
		offset += syscall.SizeofInotifyEvent
		name := string(bytes.TrimRight(buf[offset:offset+length], "\x00"))
		offset += length
		events = append(events, Event{Wd: int(wd), Mask: mask, Name: name})
	}
	return events, nil
}

// isExcluded is true if the path (relative to source) starts with the exclude pattern
func isExcluded(path string, opt *Options) bool {
	if opt.ExcludePat == "" {
		return false
	}
	relative, err := filepath.Rel(opt.SourcePath, path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(relative, strings.TrimRight(opt.ExcludePat, "*"))
}

// watchTree watches a directory and all of its sub directories
func watchTree(in *Inotify, top string, opt *Options) {
	filepath.WalkDir(top, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			// directory removed while we walk, or a file: nothing to watch
			return nil
		}
		// do not walk into excluded directories
		if path != top && isExcluded(path, opt) {
			return filepath.SkipDir
		}
		err = in.AddWatch(path)
		if errors.Is(err, syscall.ENOSPC) {
			logError("No more inotify watches for %s, raise the limit: "+
				"sysctl fs.inotify.max_user_watches", path)
		} else if err != nil && !errors.Is(err, syscall.ENOENT) {
			// ENOENT: directory was removed before we could watch it
			logWarning("Can not watch %s: %v", path, err)
		}
		return nil
	})
}

// handleEvents keeps the watches up to date.
// Returns false if the source directory is gone.
func handleEvents(in *Inotify, events []Event, opt *Options) bool {
	for _, event := range events {
		if event.Mask&syscall.IN_Q_OVERFLOW != 0 {
			// The kernel queue was full and we lost events.
			// Not a problem, rsync copies everything, but we may have
			// lost some new directories, so we watch the tree again.
			logWarning("inotify queue overflow, rescan %s", opt.SourcePath)
			watchTree(in, opt.SourcePath, opt)
			continue
		}

		directory, found := in.watches[event.Wd]
		if !found {
			continue
		}

		if event.Mask&syscall.IN_IGNORED != 0 {
			// The watched directory was removed
			delete(in.watches, event.Wd)
			if directory == opt.SourcePath {
				return false
			}
			continue
		}

		if event.Mask&syscall.IN_MOVE_SELF != 0 && directory == opt.SourcePath {
			// The source directory was renamed (moved away)
			return false
		}

		path := filepath.Join(directory, event.Name)
		logDebug("event: 0x%08x %s", event.Mask, path)

		// A new (or moved here) directory needs to be watched too
		if event.Mask&syscall.IN_ISDIR != 0 &&
			event.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 &&
			!isExcluded(path, opt) {
			watchTree(in, path, opt)
		}
	}
	return true
}

// isDir is true if path is a directory
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
