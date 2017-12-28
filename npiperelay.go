package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const cERROR_PIPE_NOT_CONNECTED syscall.Errno = 233

var (
	poll            = flag.Bool("p", false, "poll until the the named pipe exists")
	closeWrite      = flag.Bool("s", false, "send a 0-byte message to the pipe after EOF on stdin")
	closeOnEOF      = flag.Bool("ep", false, "terminate on EOF reading from the pipe, even if there is more data to write")
	closeOnStdinEOF = flag.Bool("ei", false, "terminate on EOF reading from stdin, even if there is more data to write")
	verbose         = flag.Bool("v", false, "verbose output on stderr")
)

func dialPipe(p string, poll bool) (*overlappedFile, error) {
	p16, err := windows.UTF16FromString(p)
	if err != nil {
		return nil, err
	}
	for {
		h, err := windows.CreateFile(&p16[0], windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return newOverlappedFile(h), nil
		}
		if poll && os.IsNotExist(err) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return nil, &os.PathError{Path: p, Op: "open", Err: err}
	}
}

func dialPort(p int, poll bool) (*overlappedFile, error) {
	if p < 0 || p > 65535 {
		return nil, errors.New("Invalid port value")
	}

	for {
		h, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}

		// Create a SockaddrInet4 for connecting to
		sa := &windows.SockaddrInet4{Addr: [4]byte{0x7F, 0x00, 0x00, 0x01}, Port: p}
		if err != nil {
			return nil, err
		}

		// Bind to a randomly assigned port
		err = windows.Bind(h, &windows.SockaddrInet4{})
		if err != nil {
			return nil, err
		}

		conn := newOverlappedFile(h)

		_, err = conn.asyncIo(func(h windows.Handle, n *uint32, o *windows.Overlapped) error {
			return windows.ConnectEx(h, sa, nil, 0, nil, o)
		})
		err = os.NewSyscallError("connectEx", err)

		if err == nil {
			return conn, nil
		}

		if poll && os.IsNotExist(err) {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		return nil, err
	}
}

func underlyingError(err error) error {
	if serr, ok := err.(*os.SyscallError); ok {
		return serr.Err
	}
	return err
}

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	if *verbose {
		log.Println("connecting to", args[0])
	}

	conn, err := dialPipe(args[0], *poll)
	if err != nil {
		log.Fatalln(err)
	}

	if !strings.HasPrefix("//./", args[0]) {
		tmp := make([]byte, 22) // 5 bytes for ascii port number, 1 for newline, 16 for nonce

		var port int
		nonce := make([]byte, 16)

		// Check if file is a LibAssuane socket
		_, err := conn.Read(tmp)
		if err != nil {
			log.Fatalln("Could not open socket", err)
		}

		for i, c := range tmp {
			// Find the new line
			if c == 0x0A {
				port, _ = strconv.Atoi(string(tmp[:i]))
				copy(nonce, tmp[i+1:])

				log.Printf("Port: %d, Nonce: %X", port, nonce)
				break
			}
		}

		_ = conn.Close()

		conn, err = dialPort(port, *poll)

		_, err = conn.Write(nonce)
		if err != nil {
			log.Fatal(err)
		}
	}

	if *verbose {
		log.Println("connected")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		if err != nil {
			log.Fatalln("copy from stdin to pipe failed:", err)
		}

		if *verbose {
			log.Println("copy from stdin to pipe finished")
		}

		if *closeOnStdinEOF {
			os.Exit(0)
		}

		if *closeWrite {
			// A zero-byte write on a message pipe indicates that no more data
			// is coming.
			conn.Write(nil)
		}
		os.Stdin.Close()
		wg.Done()
	}()

	_, err = io.Copy(os.Stdout, conn)
	if underlyingError(err) == windows.ERROR_BROKEN_PIPE || underlyingError(err) == cERROR_PIPE_NOT_CONNECTED {
		// The named pipe is closed and there is no more data to read. Since
		// named pipes are not bidirectional, there is no way for the other side
		// of the pipe to get more data, so do not wait for the stdin copy to
		// finish.
		if *verbose {
			log.Println("copy from pipe to stdout finished: pipe closed")
		}
		os.Exit(0)
	}

	if err != nil {
		log.Fatalln("copy from pipe to stdout failed:", err)
	}

	if *verbose {
		log.Println("copy from pipe to stdout finished")
	}

	if !*closeOnEOF {
		os.Stdout.Close()

		// Keep reading until we get ERROR_BROKEN_PIPE or the copy from stdin
		// finishes.
		go func() {
			for {
				_, err := conn.Read(nil)
				if underlyingError(err) == windows.ERROR_BROKEN_PIPE {
					if *verbose {
						log.Println("pipe closed")
					}
					os.Exit(0)
				} else if err != nil {
					log.Fatalln("pipe error:", err)
				}
			}
		}()

		wg.Wait()
	}
}
