package main

import (
	"bufio"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const cERROR_PIPE_NOT_CONNECTED syscall.Errno = 233

const WSAECONNREFUSED syscall.Errno = 10061
const WSAENETUNREACH syscall.Errno = 10051
const WSAETIMEDOUT syscall.Errno = 10060
const ERROR_CONNECTION_REFUSED syscall.Errno = 1225

var (
	poll            = flag.Bool("p", false, "poll until the the named pipe exists")
	closeWrite      = flag.Bool("s", false, "send a 0-byte message to the pipe after EOF on stdin")
	closeOnEOF      = flag.Bool("ep", false, "terminate on EOF reading from the pipe, even if there is more data to write")
	closeOnStdinEOF = flag.Bool("ei", false, "terminate on EOF reading from stdin, even if there is more data to write")
	verbose         = flag.Bool("v", false, "verbose output on stderr")
	assuan          = flag.Bool("a", false, "treat the target as a libassuan file socket (Used by GnuPG)")
)

func dialPipe(p string, poll bool) (*overlappedFile, error) {
	p16, err := windows.UTF16FromString(p)
	if err != nil {
		return nil, err
	}

	h, err := windows.CreateFile(&p16[0], windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err == nil {
		return newOverlappedFile(h), nil
	}

	return nil, err
}

func dialPort(p int, poll bool) (*overlappedFile, error) {
	if p < 0 || p > 65535 {
		return nil, errors.New("Invalid port value")
	}

	h, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	// Create a SockaddrInet4 for connecting to
	sa := &windows.SockaddrInet4{Addr: [4]byte{0x7F, 0x00, 0x00, 0x01}, Port: p}

	// Bind to a randomly assigned local port
	err = windows.Bind(h, &windows.SockaddrInet4{})
	if err != nil {
		return nil, err
	}

	// Wrap our socket up to be properly handled
	conn := newOverlappedFile(h)

	// Connect to the LibAssuan socket using overlapped ConnectEx operation
	_, err = conn.asyncIo(func(h windows.Handle, n *uint32, o *windows.Overlapped) error {
		return windows.ConnectEx(h, sa, nil, 0, nil, o)
	})
	if err == nil {
		return conn, nil
	}

	return nil, err
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

	var conn *overlappedFile
	var err error

	// Loop only if we're polling the named pipe or socket
	for {
		conn, err = dialPipe(args[0], *poll)

		if *poll && os.IsNotExist(err) {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if err != nil {
			err = &os.PathError{Path: args[0], Op: "open", Err: err}
			log.Fatalln(err)
		}

		// LibAssaaun file socket: Attempt to read contents of the target file and connect to a TCP port
		if *assuan {
			var port int
			var nonce [16]byte

			reader := bufio.NewReader(conn)

			// Read the target port number from the first line
			tmp, _, err := reader.ReadLine()
			port, err = strconv.Atoi(string(tmp))
			if err != nil {
				log.Fatalln(err)
			}

			// Read the rest of the nonce from the file
			n, err := reader.Read(nonce[:])
			if err != nil {
				log.Fatalln(err)
			}

			if n != 16 {
				log.Fatalf("Read incorrect number of bytes for nonce. Expected 16, got %d (0x%X)", n, nonce)
			}

			if *verbose {
				log.Printf("Port: %d, Nonce: %X", port, nonce)
			}

			_ = conn.Close()

			// Try to connect to the libassaun TCP socket hosted on localhost
			conn, err = dialPort(port, *poll)

			if *poll && (err == WSAETIMEDOUT || err == WSAECONNREFUSED || err == WSAENETUNREACH || err == ERROR_CONNECTION_REFUSED) {
				time.Sleep(200 * time.Millisecond)
				continue
			}

			err = os.NewSyscallError("ConnectEx", err)

			if err != nil {
				log.Fatal(err)
			}

			_, err = conn.Write(nonce[:])
			if err != nil {
				log.Fatal(err)
			}
		}
		break
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
