package agent

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// fileChunkSize is the payload size used when streaming files and command
// output. It is deliberately well below MaxFrameSize so that base64 JSON
// encoding can never push a frame over the limit.
const fileChunkSize = 32 * 1024

func (s *server) handleExec(req *Request) error {
	if len(req.Command) == 0 {
		return s.writeError("exec: empty command")
	}
	if req.TTY {
		return s.runTTY(req)
	}
	return s.runNonTTY(req)
}

// commandEnv appends extra variables to the agent's environment, or returns nil
// to inherit it unchanged.
func commandEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	return append(os.Environ(), extra...)
}

// exitCode maps a cmd.Wait error to a process exit code. A nil error is 0; an
// exited process reports its own code; anything else (including a signal) is -1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// messageWriter streams process output to the client as frames of one type.
type messageWriter struct {
	s   *server
	typ string
}

func (w messageWriter) Write(p []byte) (int, error) {
	if err := w.s.write(Message{Type: w.typ, Data: p}); err != nil {
		// The client is gone; cancel the connection context so a still-running
		// command is killed rather than orphaned.
		w.s.cancel()
		return 0, err
	}
	return len(p), nil
}

// runNonTTY runs the command with separate stdout and stderr streams.
func (s *server) runNonTTY(req *Request) error {
	cmd := exec.CommandContext(s.ctx, req.Command[0], req.Command[1:]...) //nolint:gosec // G204: executing guest commands is this package's purpose
	cmd.Env = commandEnv(req.Env)
	cmd.Dir = req.WorkDir
	cmd.Stdout = messageWriter{s: s, typ: "stdout"}
	cmd.Stderr = messageWriter{s: s, typ: "stderr"}

	if err := cmd.Start(); err != nil {
		return s.writeError("exec: start %s: %v", req.Command[0], err)
	}
	// Wait also waits for the goroutines os/exec spawns to copy output into the
	// writers above, so every output frame precedes the exit frame.
	code := exitCode(cmd.Wait())
	return s.write(Message{Type: "exit", Code: &code})
}

// runTTY runs the command attached to a pseudo-terminal. A TTY merges stderr
// into stdout, so output is streamed as a single stdout stream.
func (s *server) runTTY(req *Request) error {
	master, slave, err := openPTY()
	if err != nil {
		return s.writeError("exec: %v", err)
	}
	defer func() { _ = master.Close() }()

	cmd := exec.CommandContext(s.ctx, req.Command[0], req.Command[1:]...) //nolint:gosec // G204: executing guest commands is this package's purpose
	cmd.Env = commandEnv(req.Env)
	cmd.Dir = req.WorkDir
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = ptySysProcAttr()

	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		return s.writeError("exec: start %s: %v", req.Command[0], err)
	}
	// The child holds its own reference to the slave end.
	_ = slave.Close()

	// Forward the client's raw stdin to the pty while the command runs. Raw
	// bytes are only consumed for TTY sessions; framed requests never carry
	// stdin, so this cannot be multiplexed with later requests.
	if conn, ok := s.rw.(net.Conn); ok {
		var stdinWG sync.WaitGroup
		stdinWG.Add(1)
		go func() {
			defer stdinWG.Done()
			copyStream(conn, master)
		}()
		// Once the command exits, unblock the reader with an immediate
		// deadline so the goroutine cannot leak into the next request, then
		// clear it before ServeConn reads again.
		defer func() {
			_ = conn.SetReadDeadline(time.Now())
			stdinWG.Wait()
			_ = conn.SetReadDeadline(time.Time{})
		}()
	}

	streamFrames(s, master, "stdout")
	code := exitCode(cmd.Wait())
	return s.write(Message{Type: "exit", Code: &code})
}

// streamFrames copies r to the client as frames of the given type until EOF or
// a write failure.
func streamFrames(s *server, r io.Reader, typ string) {
	buf := make([]byte, fileChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := s.write(Message{Type: typ, Data: buf[:n]}); werr != nil {
				// The client is gone; kill any still-running command.
				s.cancel()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// copyStream copies r to w until EOF or a write failure.
func copyStream(r io.Reader, w io.Writer) {
	buf := make([]byte, fileChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
