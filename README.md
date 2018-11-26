# npiperelay

npiperelay is a tool that allows you to access a Windows named pipe in a way
that is more compatible with a variety of command-line tools. With it, you can
use Windows named pipes from the Windows Subsystem for Linux (WSL).

For example, you can:

* Connect to Docker for Windows from the Linux Docker client in WSL
* Connect to MySQL Server running as a Windows service
* Connect interactively to a Hyper-V Linux VM's serial console
* Use gdb to connect to debug the kernel of a Hyper-V Linux VM

Let me know on Twitter ([@gigastarks](https://twitter.com/gigastarks)) if you come up with more interesting uses.

# Installation

Binaries for npiperelay are not currently available. You have to build from source. With Go, this is not too difficult.

Basic steps:

1. Install Go.
2. Download and build the Windows binary and add it to your path.
3. Install socat.

## Installing Go

To build the binary, you will need a version of [Go](https://golang.org). You can use a Windows build of Go or, as outlined here, you can use a Linux build and cross-compile the Windows binary directly from WSL.

## Building npiperelay.exe

Once you have Go installed (and your GOPATH configured), you need to download and install the tool. This is a little tricky because we are building the tool for Windows from WSL:

```bash
$ go get -d github.com/jstarks/npiperelay
$ GOOS=windows go build -o /mnt/c/Users/<myuser>/go/bin/npiperelay.exe github.com/jstarks/npiperelay
```

In this example, we have put the binary in `/mnt/c/Users/<myuser>/go/bin`. We then need to make sure that this directory is available in the WSL path. This can be achieved either by adding C:\Users\<myuser>\go\bin to the Win32 path and restarting WSL, or by just adding the path directly in WSL via the command line or in our `.bash_profile` or `.bashrc`.

Or you can just symlink it into something that's already in your path:

```bash
$ sudo ln -s /mnt/c/Users/<myuser>/go/bin/npiperelay.exe /usr/local/bin/npiperelay.exe
```

You may be tempted to just put the real binary directly into `/usr/local/bin`, but this will not work because Windows currently cannot run binaries that exist in the Linux namespace -- they have to reside somewhere under the Windows portion of the file system.

## Installing socat

For all of the examples below, you will need the excellent `socat` tool. Your WSL distribution should
have it available; install it by running

```bash
$ sudo apt install socat
```

or the equivalent.

# Usage

The examples below assume you have copied the contents of the `scripts` directory (from `$HOME/go/src/github.com/jstarks/npiperelay/scripts`) into your PATH somewhere. These scripts are just examples and can be modified to suit your needs.

## Connecting to Docker from WSL

This assumes you already have the Docker daemon running in Windows, e.g. because you have installed Docker for Windows. You may already have the ability to connect to this daemon from WSL via TCP, but this has security problems because any user on your machine will be able to connect. With these steps, you'll be able to limit access to privileged users.

Basic steps:

1. Start the Docker relay.
2. Use the `docker` CLI as usual.

### Staring the Docker relay

For this to work, you will need to be running in an elevated WSL session, or you will need to configure Docker to allow your Windows user access without elevating.

You also need to be running as root within WSL, or launch the command under sudo. This is necessary because the relay will create a file /var/run/docker.sock.

```bash
$ sudo docker-relay &
```

### Using the docker CLI with the relay

At this point, ordinary `docker` commands should run fine as root. Try

```bash
$ sudo docker info
```

If this succeeds, then you are connected. Now try some other Docker commands:

```bash
$ sudo docker run -it --rm microsoft/nanoserver cmd /c "Back in Windows again..."
```

#### Running without root

The `docker-relay` script configured the Docker pipe to allow access by the
`docker` group. To run as an ordinary user, add your WSL user to the docker
group. In Ubuntu:

```bash
$ sudo adduser <my_user> docker
```

Then open a new WSL window to reset your group membership.

## Connect to MySQL Server running as a Windows service

If you run MySQL Server as a Windows service, you can configure it to
communicate through TCP, named pipes or shared memory. If you use named
pipes, connecting to MySQL from WSL is very similar to connecting
to Docker.

The `mysqld-relay` script is designed to be run in a `sudo` shell.
Before creating the relay, it will try to configure your environment
(if it has not been configured yet) by:

* creating `/var/run/mysqld/`,
* creating a `mysql` group, and
* adding your user account to the `mysql` group.

You can of course pull out just the npiperelay command if you don't
need any of the above checks.

Note that if you need to enter a password for sudo, the following
command will fail because of the lack of password input:

```bash
$ sudo mysqld-relay &
```

In that case, you can run it like this:

```bash
user@machine:~$ sudo -s
[sudo] password for user:
root@machine:~# mysqld-relay &
root@machine:~# exit
user@machine:~$ _
```

Now you can use the Linux `mysql` command line client or any other
Linux process that expects to talk to MySQL Server through
`/var/run/mysqld/mysqld.sock`.

## Connecting to a Hyper-V Linux VM's serial console

If you have a Linux VM configured in Hyper-V, you may wish to use its serial
port as a serial console. With npiperelay, this can be done fairly easily from
the command line.

Basic steps:

1. Enable the serial port for your Linux VM.
2. Configure your VM to run the console on the serial port.
3. Run socat to relay between your terminal and npiperelay.

### Enabling the serial port

This is easiest to do from the command line, via the Hyper-V PowerShell cmdlets.
You'll need to add your user to the Hyper-V Administrators group or run the
command line elevated for this to work.

If you have a VM named `foo` and you want to enable the console on COM1 (/dev/ttyS0), with a named pipe name of `foo_debug_pipe`:

```bash
$ powershell.exe Set-VMComPort foo 1 '\\.\pipe\foo_debug_pipe'
```

### Configuring your VM to run the console on the serial port

Refer to your VM Linux distribution's instructions for enabling the serial console:

* [Ubuntu](https://help.ubuntu.com/community/SerialConsoleHowto)
* [Fedora](https://docs.fedoraproject.org/f26/system-administrators-guide/kernel-module-driver-configuration/Working_with_the_GRUB_2_Boot_Loader.html#sec-GRUB_2_over_a_Serial_Console])

### Connecting to the serial port

For this step, WSL must be running elevated or your Windows user must be in the
Hyper-V Administrators group.

#### Directly via socat

The easiest approach is to use socat to connect directly. The `vmserial-connect` script does this and even looks up the pipe name from the VM name and COM port for you:

```bash
$ vmserial-connect foo 1
<enter>
Ubuntu 17.04 gigastarks-vm ttyS0

gigastarks-vm login:
```

Press Ctrl-O to exit the connection and return to your shell.

#### Via screen

If you prefer to use a separate tool to connect to the device such as `screen`, then you must run a separate `socat` process to relay between the named pipe and a PTY. The `serial-relay` script does this
for you with the right parameters; simply run:

```bash
$ serial-relay //./pipe/foo_debug_pipe $HOME/foo-pty & # Starts the relay
$ screen $HOME/foo-pty                                 # Attaches to the serial terminal
```

See the `screen` documentation (`man screen`) for more details.

## Debugging the kernel of a Hyper-V Linux VM

Follow the same steps to enable the COM port for your VM, then run the serial
relay as though you were going to run `screen` to connect to the serial console.

Next, run gdb and connect to the serial port:

```bash
gdb ./vmlinux
target remote /home/<myuser>/foo-pty
```

## Custom usage

Take a look at the scripts for sample usage, or run `npiperelay.exe` without any parameters for parameter documentation.