package main

// sandbox cp: moving files and trees between this machine and a sandbox's workspace.
//
// It is one command for both directions, and which direction it is comes from the argument
// that names a sandbox: `sandbox cp <id>:<guest-path> <local-path>` copies out, and
// `sandbox cp <local-path> <id>:<guest-path>` copies in. The guest path names what is
// created or read, relative to the workspace root the guest confines every file operation
// to (a leading "/" is that root), so the two are symmetric: what a copy out wrote at a path
// is what a copy in of the same path replaces.
//
// A file and a tree are both supported, in both directions, and neither is buffered: a
// directory is streamed as a tar archive through a pipe, so a workspace of any size costs a
// chunk of memory rather than its own size. That is also why this cannot simply use
// archive/tar on a whole file: the point of the operation is a repository-sized tree.
//
// # What an archive may write
//
// An archive from a sandbox is untrusted input even when the caller trusts the sandbox: it
// can carry entries that mean "write somewhere else". So the local extractor holds the same
// rules the guest agent holds when one of these archives arrives there — a name that leaves
// the destination is refused, a symbolic link is written only when its target is relative and
// stays inside, a hard link only when it names a file the same archive wrote, and a device,
// fifo or socket is skipped and counted — and it writes through os.Root, so the kernel's own
// openat walk enforces the boundary rather than a string comparison.

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

func newSandboxCopyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp <id>:<path> <local-path> | cp <local-path> <id>:<path>",
		Short: "Copy a file or a tree between a sandbox and this machine",
		Long: `Copy a file or a tree between a sandbox's workspace and this machine.

Exactly one of the two arguments names a sandbox, as <id>:<guest-path>. The guest path is
relative to the sandbox's workspace root, which is the only directory its guest agent will
read or write (a leading "/" is that root), and it names what is created or read: a copy in
writes the local file, or the local directory's tree, at that path.

A directory is copied as a tar archive streamed in both directions, so a repository-sized
tree costs no more memory than a chunk of it. A path that is neither a file nor a
directory — a symbolic link, a device, a socket — is refused: a copy of a link would not be
a copy of what it points at.

An archive arriving from a sandbox is untrusted: entries that would write outside the
destination are refused, links are written only when they stay inside, and entries this
machine cannot hold (a device, a fifo) are skipped and counted.

Writing a tree needs the template's agent to speak the untar operation. A template built
before it answers with the capability it lacks, and a single file still arrives there.`,
		Example: `  swarmcracker sandbox cp 5f3a1b2c9d:workspace/app.log ./app.log
  swarmcracker sandbox cp ./repo 5f3a1b2c9d:workspace/repo
  swarmcracker sandbox cp 5f3a1b2c9d:workspace ./backup`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			fromID, fromPath, fromSandbox := splitSandboxRef(args[0])
			toID, toPath, toSandbox := splitSandboxRef(args[1])
			if fromSandbox == toSandbox {
				return output.NewError(output.CodeUsage,
					"exactly one of the two paths must name a sandbox, as <id>:<path>").
					WithDetails(map[string]any{"reason": "sandbox_reference_required"})
			}

			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			if fromSandbox {
				return sandboxCopyOut(cmd, c, fromID, fromPath, args[1])
			}
			return sandboxCopyIn(cmd, c, args[0], toID, toPath)
		},
	}
	return cmd
}

// splitSandboxRef splits an argument that names a sandbox from one that names a local path.
//
// The rule is Docker's, and it is a rule rather than a guess: a reference is <id>:<path>,
// where the part before the colon has no path separator (so "/tmp/a:b" and "C:\a" stay local
// paths) and the part after it is not empty.
func splitSandboxRef(arg string) (id, guestPath string, ok bool) {
	colon := strings.Index(arg, ":")
	if colon <= 0 {
		return "", "", false
	}
	if strings.ContainsAny(arg[:colon], `/\`) {
		return "", "", false
	}
	rest := strings.TrimSpace(arg[colon+1:])
	if rest == "" || strings.ContainsAny(arg[:colon], " \t") {
		return "", "", false
	}
	// A Windows drive letter is a local path: "C:\a" names a file on this machine, not a
	// sandbox called C.
	if len(arg[:colon]) == 1 && strings.HasPrefix(rest, `\`) {
		return "", "", false
	}
	// A leading "/" is the guest's workspace root, which is where every guest path is
	// relative to. The agent confines every file operation to it, so the two spellings
	// mean the same thing and both are accepted.
	return arg[:colon], strings.TrimPrefix(path.Clean(rest), "/"), true
}

// sandboxCopyOut copies one guest path onto this machine.
//
// A file goes to the local path, or into it when that path is an existing directory; a
// directory's tree goes to the local path, which is created if it does not exist.
func sandboxCopyOut(cmd *cobra.Command, c *client.SandboxClient, id, guestPath, localPath string) error {
	kind, body, err := c.GetSandboxFile(cmd.Context(), id, client.SandboxFileQuery{Path: guestPath})
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	out := cmd.OutOrStdout()

	switch kind {
	case client.SandboxFileKindFile:
		target, err := localFileTarget(localPath, guestPath)
		if err != nil {
			return err
		}
		file, err := os.Create(target)
		if err != nil {
			return output.NewErrorf(output.CodeUsage, "create %s: %v", target, err)
		}
		written, copyErr := io.Copy(file, body)
		closeErr := file.Close()
		if copyErr != nil {
			return output.NewErrorf(output.CodeUnavailable, "write %s: %v", target, copyErr)
		}
		if closeErr != nil {
			return output.NewErrorf(output.CodeUnavailable, "write %s: %v", target, closeErr)
		}
		fmt.Fprintf(out, "%s: copied %d bytes to %s\n", guestPath, written, target)
		return nil

	case client.SandboxFileKindDir:
		target, err := localDirectoryTarget(localPath, guestPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return output.NewErrorf(output.CodeUsage, "create %s: %v", target, err)
		}

		// The archive's names are relative to the guest's file root and carry the path
		// that was asked for, so that prefix is stripped: the local path names the
		// tree, exactly as the guest path named it on the other side.
		summary, err := extractLocalTree(body, target, guestPath)
		if err != nil {
			return output.NewErrorf(output.CodeUnavailable, "write %s: %v", target, err)
		}
		fmt.Fprintf(out, "%s: extracted %d entries (%d bytes) into %s\n",
			guestPath, summary.entries, summary.bytes, target)
		if summary.skipped > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: skipped %d entries this machine cannot hold\n",
				guestPath, summary.skipped)
		}
		return nil

	default:
		return output.NewErrorf(output.CodeInvalid,
			"%s is neither a file nor a directory (the guest reported %q), and only those can be copied",
			guestPath, kind)
	}
}

// sandboxCopyIn copies one local path into a sandbox: a regular file through the agent's put,
// and a directory as a tar archive streamed through a pipe.
func sandboxCopyIn(cmd *cobra.Command, c *client.SandboxClient, localPath, id, guestPath string) error {
	info, err := os.Lstat(localPath)
	if err != nil {
		return output.NewErrorf(output.CodeUsage, "read %s: %v", localPath, err)
	}

	switch {
	case info.Mode().IsRegular():
		file, err := os.Open(localPath)
		if err != nil {
			return output.NewErrorf(output.CodeUsage, "read %s: %v", localPath, err)
		}
		defer func() { _ = file.Close() }()

		doc, err := c.PutSandboxFile(cmd.Context(), id, client.SandboxFileQuery{
			Path: guestPath,
			Mode: uint32(info.Mode().Perm()),
		}, file)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: wrote %d bytes to %s:%s\n", localPath, doc.Bytes, id, guestPath)
		return nil

	case info.IsDir():
		// The tree is built as it is streamed: the pipe lets the walk write while the
		// request reads, so neither side ever holds the tree.
		reader, writer := io.Pipe()
		type buildResult struct {
			skipped int
			err     error
		}
		built := make(chan buildResult, 1)
		go func() {
			skipped, err := streamLocalTree(writer, localPath, guestPath)
			_ = writer.CloseWithError(err)
			built <- buildResult{skipped: skipped, err: err}
		}()

		doc, err := c.PutSandboxTree(cmd.Context(), id, client.SandboxFileQuery{Path: guestPath}, reader)
		if err != nil {
			return err
		}
		build := <-built
		if build.err != nil {
			return output.NewErrorf(output.CodeUsage, "read %s: %v", localPath, build.err)
		}
		if build.skipped > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"%s: skipped %d entries that are not a file, a directory or an inside link\n",
				localPath, build.skipped)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s: wrote %d entries (%d bytes) to %s:%s\n",
			localPath, doc.Entries, doc.Bytes, id, guestPath)
		for _, warning := range doc.Warnings {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s:%s: %s\n", id, guestPath, warning)
		}
		return nil

	default:
		return output.NewErrorf(output.CodeUsage,
			"%s is neither a regular file nor a directory, and only those can be copied", localPath)
	}
}

// localFileTarget is where a copied-out file lands: the local path itself, or inside it when
// that path is an existing directory (in which case the file keeps the name it had).
func localFileTarget(localPath, guestPath string) (string, error) {
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		return filepath.Join(localPath, filepath.Base(path.Clean(guestPath))), nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", output.NewErrorf(output.CodeUsage, "read %s: %v", localPath, err)
	}
	if strings.HasSuffix(localPath, string(os.PathSeparator)) {
		return "", output.NewErrorf(output.CodeUsage,
			"%s does not exist: name the file to write, not a directory to write into", localPath)
	}
	return localPath, nil
}

// localDirectoryTarget is where a copied-out tree lands. A local path that does not exist is
// created as the tree; an existing directory receives the tree under the name it had in the
// sandbox, which is what makes `cp <id>:workspace ./backup` put a tree inside ./backup rather
// than replacing it.
func localDirectoryTarget(localPath, guestPath string) (string, error) {
	base := filepath.Base(path.Clean(guestPath))
	if info, err := os.Stat(localPath); err == nil {
		if !info.IsDir() {
			return "", output.NewErrorf(output.CodeUsage,
				"%s is not a directory, and the tree at %s cannot be written into it", localPath, guestPath)
		}
		return filepath.Join(localPath, base), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", output.NewErrorf(output.CodeUsage, "read %s: %v", localPath, err)
	}
	return localPath, nil
}

// localTreeSummary is what an extraction wrote.
type localTreeSummary struct {
	entries int
	bytes   int64
	// skipped counts the entries this machine cannot hold: a device, a fifo, a socket.
	skipped int
}

// extractLocalTree writes the tar archive r carries into dest, keeping only what is under
// strip — the guest path that was asked for, which the archive's names carry.
//
// Every write goes through an os.Root, so a name that leaves dest is refused by the kernel's
// own openat walk; the checks here are for the things a root cannot express: an entry outside
// the tree that was asked for, and a link that would point out of it.
func extractLocalTree(r io.Reader, dest, strip string) (localTreeSummary, error) {
	var summary localTreeSummary

	root, err := os.OpenRoot(dest)
	if err != nil {
		return summary, err
	}
	defer func() { _ = root.Close() }()

	prefix := path.Clean(strip)
	if prefix == "." || prefix == "/" {
		prefix = ""
	}
	// written records the regular files this archive created, so a hard link can be
	// checked against the archive rather than against whatever was in dest already.
	written := map[string]bool{}

	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, fmt.Errorf("read the archive: %w", err)
		}

		name, err := localArchiveName(header.Name, prefix)
		if err != nil {
			return summary, err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if name == "." {
				summary.entries++
				continue
			}
			// MkdirAll takes permission bits and nothing else: the directory bit is what
			// the entry's type already says, and passing it is an error, not a widening.
			if err := root.MkdirAll(name, archivePerm(header)); err != nil {
				return summary, fmt.Errorf("create %s: %w", name, err)
			}
			summary.entries++

		case tar.TypeReg:
			parent := path.Dir(name)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return summary, fmt.Errorf("create %s: %w", parent, err)
				}
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, archivePerm(header))
			if err != nil {
				return summary, fmt.Errorf("create %s: %w", name, err)
			}
			bytes, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return summary, fmt.Errorf("write %s: %w", name, copyErr)
			}
			if closeErr != nil {
				return summary, fmt.Errorf("write %s: %w", name, closeErr)
			}
			summary.entries++
			summary.bytes += bytes
			written[name] = true

		case tar.TypeSymlink:
			if !linkStaysInside(name, header.Linkname) {
				return summary, fmt.Errorf("%s is a symbolic link to %q, which leaves %s", header.Name, header.Linkname, dest)
			}
			if err := replaceWithLink(root, name, func() error { return root.Symlink(header.Linkname, name) }); err != nil {
				return summary, fmt.Errorf("link %s: %w", name, err)
			}
			summary.entries++

		case tar.TypeLink:
			target, err := localArchiveName(header.Linkname, prefix)
			if err != nil {
				return summary, err
			}
			if !written[target] {
				return summary, fmt.Errorf("%s is a hard link to %q, which this archive did not write", header.Name, header.Linkname)
			}
			if err := replaceWithLink(root, name, func() error { return root.Link(target, name) }); err != nil {
				return summary, fmt.Errorf("link %s: %w", name, err)
			}
			summary.entries++

		default:
			// A device, a fifo, a socket: nothing this machine should create from an
			// archive, and nothing a workspace copy needs.
			summary.skipped++
		}
	}
	return summary, nil
}

// localArchiveName is where one archive entry lands: its name with the tree that was asked
// for stripped, refused when it names anything outside that tree.
func localArchiveName(name, prefix string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("the archive names %q, which is outside the destination", name)
	}
	if prefix != "" {
		if clean != prefix && !strings.HasPrefix(clean, prefix+"/") {
			return "", fmt.Errorf("the archive names %q, which is outside the tree that was asked for (%s)", name, prefix)
		}
		clean = strings.TrimPrefix(strings.TrimPrefix(clean, prefix), "/")
	}
	if clean == "" {
		clean = "."
	}
	return clean, nil
}

// replaceWithLink removes whatever is at name first, so a link replaces a file the way a
// directory entry with the same name does in an archive.
func replaceWithLink(root *os.Root, name string, create func() error) error {
	if _, err := root.Lstat(name); err == nil {
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	return create()
}

// archivePerm is the permission bits of one entry and nothing else: an archive does not get
// to set ownership, and a type bit belongs to the entry's typeflag rather than here — MkdirAll
// and OpenFile refuse a mode that carries one.
func archivePerm(header *tar.Header) os.FileMode {
	// Only the twelve permission bits are wanted, and masking them first is what bounds the
	// conversion: an archive may carry anything in this field.
	return os.FileMode(header.Mode & 0o7777).Perm() //nolint:gosec // G115: masked to twelve bits above
}

// linkStaysInside reports whether a symbolic link at rel pointing at target is still inside
// the destination. The link is not followed — nothing here resolves a path — so the question
// is what the link would mean to a reader: a relative target that, joined with the link's
// own directory, does not climb out.
func linkStaysInside(rel, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return false
	}
	joined := path.Clean(path.Join(path.Dir(rel), target))
	return joined != ".." && !strings.HasPrefix(joined, "../")
}

// streamLocalTree writes localPath into w as a tar archive whose names are rooted at
// guestBase — the guest path the tree was asked for, which every entry must be under for the
// guest to accept it. It reports how many entries it left out.
//
// Ownership is left at the zero value: a workspace belongs to the guest's own user, and an
// archive that claimed to own a file does not make it so there either. A link whose target
// would leave the tree is skipped and counted, because the guest would refuse it and a copy
// that silently dropped it would be worse than one that says so.
func streamLocalTree(w io.Writer, localPath, guestBase string) (int, error) {
	writer := tar.NewWriter(w)
	defer func() { _ = writer.Close() }()

	base := path.Clean(strings.TrimPrefix(guestBase, "/"))
	if base == "." || base == "/" {
		return 0, fmt.Errorf("a tree needs a guest path to be written at")
	}
	if err := writer.WriteHeader(&tar.Header{
		Name: base + "/", Typeflag: tar.TypeDir, Mode: 0o755,
	}); err != nil {
		return 0, err
	}

	var skipped int
	err := filepath.Walk(localPath, func(full string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localPath, full)
		if err != nil {
			return err
		}
		if rel == "." {
			// The tree's own directory entry is already written, before the walk: an
			// archive whose first entry is the tree is what the guest expects to see.
			return nil
		}

		name := path.Join(base, filepath.ToSlash(rel))

		link := ""
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if !linkStaysInside(name, target) {
				skipped++
				return nil
			}
			link = target
		case info.IsDir(), info.Mode().IsRegular():
		default:
			// A device, a fifo or a socket: the guest cannot hold it either, and
			// sending a header it will only refuse would be noise.
			skipped++
			return nil
		}

		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
		}
		// The guest's own user owns what it writes; a copy does not carry the
		// operator's uid and gid into the workspace.
		header.Uid, header.Gid = 0, 0
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(full)
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, file); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
	if err != nil {
		return 0, err
	}
	return skipped, nil
}
