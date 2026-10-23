package apihttp

import "os"

// writeTempFile writes data to a new temporary file inside dir whose name
// matches pattern, sets mode, fsyncs the data and closes it. It returns the
// temporary path; the caller renames it into place or removes it. On failure
// the temporary file is removed.
func writeTempFile(dir, pattern string, data []byte, mode os.FileMode) (string, error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}

	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}
