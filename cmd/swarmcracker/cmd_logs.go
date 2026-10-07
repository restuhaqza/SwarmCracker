package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/spf13/cobra"
)

var (
	logsFollow bool
	logsTail   int
	logsSince  string
)

// maxLogLineBytes bounds a single log line. Firecracker logs API request bodies
// on one line, which can be large; the default 64 KiB scanner limit is too
// small and would abort reading the file.
const maxLogLineBytes = 1024 * 1024

// newLogsCommand creates the logs command
func newLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <vm-id>",
		Short: "View VM logs",
		Long: `Display logs from a microVM.

This command shows the serial-console and VMM output for a microVM. It works for
VMs created with 'swarmcracker vm create -d' and for service tasks managed by the
daemon (the daemon mirrors each VM's console to <log-dir>/<task-id>.log).

Example:
  swarmcracker vm logs nginx-1
  swarmcracker vm logs --follow nginx-1
  swarmcracker vm logs --tail 100 nginx-1
  swarmcracker vm logs --since 1h nginx-1
  swarmcracker vm logs --since 2026-01-02T15:04:05 nginx-1`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(args[0])
		},
	}

	cmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVar(&logsTail, "tail", -1, "Show last N lines (default: all)")
	cmd.Flags().StringVar(&logsSince, "since", "", "Show logs since a duration (1h, 30m) or RFC3339 timestamp")

	return cmd
}

// runLogs executes the logs command
func runLogs(vmID string) error {
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	logPath, err := resolveLogPath(stateMgr, vmID)
	if err != nil {
		return err
	}

	sinceTime, err := parseSince(logsSince)
	if err != nil {
		return fmt.Errorf("invalid --since value: %w", err)
	}

	if logsFollow {
		return followLogs(logPath, logsTail, sinceTime)
	}
	return displayLogs(logPath, logsTail, sinceTime)
}

// resolveLogPath finds the log file for a VM. It prefers the path recorded in
// CLI state (for VMs created with `vm create -d`), then falls back to the
// conventional <log-dir>/<id>.log locations. The latter makes logs from
// daemon-managed service tasks — which are not persisted in CLI state —
// readable by ID.
func resolveLogPath(stateMgr *runtime.StateManager, vmID string) (string, error) {
	var candidates []string
	if st, err := stateMgr.Get(vmID); err == nil && st.LogPath != "" {
		candidates = append(candidates, st.LogPath)
	}
	candidates = append(candidates,
		filepath.Join(stateMgr.GetLogDir(), vmID+".log"),
		filepath.Join("/var/log/swarmcracker", vmID+".log"),
		filepath.Join("/tmp", "swarmcracker-"+vmID+".log"),
	)

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	if _, err := stateMgr.Get(vmID); err != nil {
		return "", fmt.Errorf("VM not found: %s", vmID)
	}
	return "", fmt.Errorf("log file not found for VM %s (looked in: %s)", vmID, strings.Join(candidates, ", "))
}

// displayLogs prints a log file once, honoring --tail and --since.
func displayLogs(logPath string, tail int, since time.Time) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	return writeBacklog(out, f, tail, since)
}

// writeBacklog writes matching lines from r to out: the last `tail` lines when
// tail >= 0, every matching line when tail < 0, and nothing when tail == 0.
func writeBacklog(out io.Writer, r io.Reader, tail int, since time.Time) error {
	if tail == 0 {
		return nil
	}
	if tail < 0 {
		return writeFiltered(out, r, since)
	}

	lines, err := tailLines(r, tail, since)
	if err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	return nil
}

// writeFiltered streams every line that passes the since filter.
func writeFiltered(out io.Writer, r io.Reader, since time.Time) error {
	sc := newLogScanner(r)
	for sc.Scan() {
		if line := sc.Text(); shouldDisplayLine(line, since) {
			fmt.Fprintln(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("error reading log file: %w", err)
	}
	return nil
}

// tailLines returns the last n lines that pass the since filter. n <= 0 yields
// no lines.
func tailLines(r io.Reader, n int, since time.Time) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}

	sc := newLogScanner(r)
	buf := make([]string, 0, n)
	for sc.Scan() {
		line := sc.Text()
		if !shouldDisplayLine(line, since) {
			continue
		}
		if len(buf) == n {
			copy(buf, buf[1:])
			buf = buf[:n-1]
		}
		buf = append(buf, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("error reading log file: %w", err)
	}
	return buf, nil
}

// followLogs prints the existing backlog (honoring --tail/--since) and then
// streams lines appended to the file until interrupted.
func followLogs(logPath string, tail int, since time.Time) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	// Replay the backlog so `-f` behaves like `logs -f` (history, then follow)
	// rather than `tail -f`.
	if err := writeBacklog(out, f, tail, since); err != nil {
		return err
	}

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}
	offset := info.Size()

	fmt.Fprintf(out, "Following logs for %s (Ctrl+C to exit)\n", logPath)
	out.Flush()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	return tailStream(f, logPath, out, offset, since, ticker.C, sigCh)
}

// tailStream reads bytes appended past offset, printing complete lines, until
// stop is closed. ticks drives polling; it is a parameter so tests can drive
// the loop deterministically.
func tailStream(f *os.File, logPath string, out *bufio.Writer, offset int64, since time.Time, ticks <-chan time.Time, stop <-chan os.Signal) error {
	var partial []byte
	for {
		select {
		case <-stop:
			if len(partial) > 0 {
				fmt.Fprintln(out, strings.TrimRight(string(partial), "\r"))
			}
			fmt.Fprintln(out, "\nStopped following logs")
			out.Flush()
			return nil
		case <-ticks:
			info, err := os.Stat(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue // transient during rotation
				}
				return fmt.Errorf("log file error: %w", err)
			}

			if info.Size() < offset {
				// Truncated or rotated: restart from the beginning.
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return fmt.Errorf("failed to seek file: %w", err)
				}
				offset = 0
				partial = nil
				fmt.Fprintln(out, "[Log file rotated]")
				out.Flush()
			}

			if info.Size() <= offset {
				continue
			}

			data := make([]byte, info.Size()-offset)
			n, err := f.ReadAt(data, offset)
			if n > 0 {
				offset += int64(n)
				partial = append(partial, data[:n]...)
				for {
					idx := bytes.IndexByte(partial, '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(string(partial[:idx]), "\r")
					if shouldDisplayLine(line, since) {
						fmt.Fprintln(out, line)
					}
					partial = partial[idx+1:]
				}
				out.Flush()
			}
			if err != nil && err != io.EOF {
				return fmt.Errorf("failed to read log file: %w", err)
			}
		}
	}
}

// newLogScanner returns a line scanner with a buffer large enough for
// Firecracker's longest single-line records.
func newLogScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
	return sc
}

// shouldDisplayLine reports whether a log line passes the since filter. Lines
// without a recognizable timestamp are kept, since they cannot be placed in
// time (e.g. kernel monotonic-timestamp lines).
func shouldDisplayLine(line string, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	ts, ok := extractTimestamp(line)
	if !ok {
		return true
	}
	return !ts.Before(since)
}

// timestampLayouts are the wall-clock formats found in VM logs. Firecracker and
// the guest do not emit a timezone, so these are parsed in local time.
var timestampLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// extractTimestamp pulls a wall-clock timestamp from the start of a log line.
// It returns false for lines without one (for example kernel lines prefixed
// with a monotonic "[    0.000000]" timestamp).
func extractTimestamp(line string) (time.Time, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return time.Time{}, false
	}

	// Candidates: a single token, or a date token joined with the next token
	// when the format separates date and time with a space.
	candidates := []string{fields[0]}
	if len(fields) > 1 {
		candidates = append(candidates, fields[0]+" "+fields[1], fields[0]+"T"+fields[1])
	}

	for _, c := range candidates {
		for _, layout := range timestampLayouts {
			if t, err := time.ParseInLocation(layout, c, time.Local); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// parseSince converts a --since value into an absolute cutoff time. It accepts
// a duration relative to now ("1h", "30m", or a bare number of minutes) or an
// RFC3339/date timestamp. An empty value yields the zero time (no filtering).
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	absoluteLayouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range absoluteLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}

	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if i, err := strconv.Atoi(s); err == nil {
		return time.Now().Add(-time.Duration(i) * time.Minute), nil
	}

	return time.Time{}, fmt.Errorf("invalid duration or timestamp: %s", s)
}
