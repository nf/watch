package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

var args []string
var needrun = make(chan bool, 1)

var kq struct {
	fd   int
	dir  *os.File
	m    map[string]*os.File
	name map[int]string
}

func kadd(fd int) {
	kbuf := make([]syscall.Kevent_t, 1)
	kbuf[0] = syscall.Kevent_t{
		Ident:  uint64(fd),
		Filter: syscall.EVFILT_VNODE,
		Flags:  syscall.EV_ADD | syscall.EV_RECEIPT | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_DELETE | syscall.NOTE_EXTEND | syscall.NOTE_WRITE,
	}
	n, err := syscall.Kevent(kq.fd, kbuf[:1], kbuf[:1], nil)
	if err != nil {
		log.Fatalf("kevent: %v", err)
	}
	ev := &kbuf[0]
	if n != 1 || (ev.Flags&syscall.EV_ERROR) == 0 || int(ev.Ident) != int(fd) || int(ev.Filter) != syscall.EVFILT_VNODE {
		log.Fatal("kqueue phase error")
	}
	if ev.Data != 0 {
		log.Fatalf("kevent: kqueue error %s", syscall.Errno(ev.Data))
	}
}

func clear() {
	os.Stdout.Write([]byte("\x1B[2J\x1B[;H"))
}

func main() {
	flag.Parse()
	args = flag.Args()
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s command\n", os.Args[0])
		os.Exit(2)
	}

	clear()
	needrun <- true
	go runner()

	var err error
	kq.fd, err = syscall.Kqueue()
	if err != nil {
		log.Fatal(err)
	}
	kq.m = make(map[string]*os.File)
	kq.name = make(map[int]string)

	dir, err := os.Open(".")
	if err != nil {
		log.Fatal(err)
	}
	kq.dir = dir
	kadd(int(dir.Fd()))
	readdir := true

	for {
		if readdir {
			kq.dir.Seek(0, 0)
			names, err := kq.dir.Readdirnames(-1)
			if err != nil {
				log.Fatalf("readdir: %v", err)
			}
			for _, name := range names {
				if ignoreFile(name) {
					continue
				}
				if kq.m[name] != nil {
					continue
				}
				f, err := os.Open(name)
				if err != nil {
					continue
				}
				kq.m[name] = f
				fd := int(f.Fd())
				kq.name[fd] = name
				kadd(fd)
			}
		}

		kbuf := make([]syscall.Kevent_t, 1)
		var n int
		for {
			n, err = syscall.Kevent(kq.fd, nil, kbuf[:1], nil)
			if err == syscall.EINTR {
				continue
			}
			break
		}
		if err != nil {
			log.Fatalf("kevent wait: %v", err)
		}
		ev := &kbuf[0]
		if n != 1 || int(ev.Filter) != syscall.EVFILT_VNODE {
			log.Fatal("kqueue phase error")
		}

		fd := int(ev.Ident)

		if !ignoreFile(kq.name[fd]) {
			select {
			case needrun <- true:
			default:
			}
			time.Sleep(100 * time.Millisecond)
		}

		readdir = fd == int(kq.dir.Fd())
		kadd(fd)
	}
}

func ignoreFile(n string) bool {
	switch {
	case strings.HasPrefix(n, "."):
	default:
		return false
	}
	return true
}

var run struct {
	sync.Mutex
	id int
}

func runner() {
	var lastcmd *exec.Cmd
	killed := make(chan bool, 1)
	for _ = range needrun {
		run.Lock()
		run.id++
		id := run.id
		run.Unlock()
		if lastcmd != nil {
			lastcmd.Process.Kill()
			<-killed
		}
		lastcmd = nil
		cmd := exec.Command(args[0], args[1:]...)
		r, w, err := os.Pipe()
		if err != nil {
			log.Fatal(err)
		}
		clear()
		fmt.Printf("$ %s\n", strings.Join(args, " "))
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Start(); err != nil {
			r.Close()
			w.Close()
			fmt.Printf("%s: %s\n", strings.Join(args, " "), err)
			continue
		}
		lastcmd = cmd
		w.Close()
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if err != nil {
					break
				}
				run.Lock()
				if id == run.id {
					os.Stdout.Write(buf[:n])
				}
				run.Unlock()
			}
			if err := cmd.Wait(); err != nil {
				run.Lock()
				if id == run.id {
					fmt.Printf("%s: %s\n", strings.Join(args, " "), err)
				}
				run.Unlock()
			}
			fmt.Println("$")
			select {
			case killed <- true:
			default:
			}
		}()
	}
}
