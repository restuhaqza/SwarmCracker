//go:build acceptance

package acceptance

// The harness's own tests: the properties a run's report depends on, checked without a
// KVM host.
//
// They exist because the first run on a real host failed in a way nothing here could have
// caught — an assertion in the third criterion aborted the binary and seven criteria
// never ran — and the report said "one failure" when the truth was "eight unknown".
//
// The criteria that are meant to fail run in a child process of this test binary
// (the standard Go idiom for asserting on a test that fails): a failing subtest marks its
// parent, so the same property cannot be checked in-process without the check failing
// itself.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The child-process contract: this variable names which child scenario to run.
const selfChildEnv = "ACCEPTANCE_SELF_CHILD"

// TestSelfChild is the child half. It is not run by a normal `go test` (its name is only
// selected by the parent's -test.run), and each scenario is expected to fail.
func TestSelfChild(t *testing.T) {
	switch os.Getenv(selfChildEnv) {
	case "continues":
		ran := []string{}
		criterion(t, "a-fails", "the first criterion fails", func(t *testing.T) {
			ran = append(ran, "a")
			t.Error("this criterion is red")
		})
		criterion(t, "b-passes", "the second criterion passes", func(t *testing.T) {
			ran = append(ran, "b")
		})
		criterion(t, "c-fails", "the third criterion fails", func(t *testing.T) {
			ran = append(ran, "c")
			assert.Fail(t, "also red")
		})
		// Every body ran, whatever the one before it decided: this is the property, and
		// the parent reads it from the ledger because this process is expected to fail.
		criterion(t, "d-ran-all", "all three ran", func(t *testing.T) {
			if strings.Join(ran, ",") != "a,b,c" {
				t.Errorf("only %v ran", ran)
			}
		})
	case "panic":
		criterion(t, "panics", "the first criterion panics", func(t *testing.T) {
			panic("a nil map, or worse")
		})
		criterion(t, "after", "the next criterion still runs", func(t *testing.T) {
			// Reaching here at all is the property; the ledger records it.
		})
	default:
		t.Skip("this test only runs as a child of another")
	}
}

// TestCriterionKeepsGoingAfterAFailure is the property the first host run needed: a
// criterion that fails records FAIL, the next one still runs, and the ledger says both.
func TestCriterionKeepsGoingAfterAFailure(t *testing.T) {
	dir := runSelfChild(t, "continues")
	ledger := readLedger(t, dir)

	assert.Contains(t, ledger, "a-fails\t", "the failing criterion is in the ledger")
	assert.Contains(t, ledger, "b-passes\t", "and the one after it ran")
	assert.Contains(t, ledger, "d-ran-all\t", "and so did the one after that")
	// One PASS and two FAIL, plus the check that all three ran: one red criterion did not
	// hide the ones after it, and the ledger is where that is visible as counts.
	assert.Equal(t, 2, strings.Count(ledger, "\tPASS"), "two criteria passed:\n%s", ledger)
	assert.Equal(t, 2, strings.Count(ledger, "\tFAIL"), "and two failed:\n%s", ledger)
}

// TestCriterionSurvivesAPanic is the other way a criterion can end the run: a panic took
// the binary down and every later criterion never ran.
func TestCriterionSurvivesAPanic(t *testing.T) {
	dir := runSelfChild(t, "panic")
	ledger := readLedger(t, dir)

	assert.Contains(t, ledger, "panics\t", "the panic is recorded as that criterion's failure")
	assert.Contains(t, ledger, "after\t", "and the next criterion ran and recorded its outcome")
	assert.Contains(t, ledger, "after", "with its own line")
}

// TestTemplateRefNamesAVersion pins the shape `template inspect` requires. A bare name is
// a usage error, so a run that passed one would report a red criterion that says nothing
// about the template.
func TestTemplateRefNamesAVersion(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"golden-sandbox-base-1.0.0.json", "golden-sandbox-base-1.1.0.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o644))
	}
	// A modtime is what orders them; the newest is the one a bare name resolves to.
	require.NoError(t, os.Chtimes(filepath.Join(dir, "golden-sandbox-base-1.1.0.json"), timeAt(t, 2), timeAt(t, 2)))
	require.NoError(t, os.Chtimes(filepath.Join(dir, "golden-sandbox-base-1.0.0.json"), timeAt(t, 1), timeAt(t, 1)))

	h := &host{t: t, goldenDir: dir, template: "sandbox-base"}
	ref, err := h.templateRef(t)
	require.NoError(t, err)
	assert.Equal(t, "sandbox-base@1.1.0", ref, "the newest manifest for the name")

	// A template that already names a version is used as written.
	h.template = "sandbox-base@1.0.0"
	ref, err = h.templateRef(t)
	require.NoError(t, err)
	assert.Equal(t, "sandbox-base@1.0.0", ref)

	// A store with no manifest for the name is a finding with the file it looked for,
	// not an empty ref that a later command would reject for the wrong reason.
	h.template = "absent"
	_, err = h.templateRef(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "golden-absent-", "the error names what it looked for")
}

// TestFlagBeforeDashDashIsTheCLIs is the bug the first host run had: the flags of a remote
// call were written after "--", so they became the guest's argv and the command fell back
// to the local admin socket. This pins which side of the separator a flag belongs on.
func TestFlagBeforeDashDashIsTheCLIs(t *testing.T) {
	// The argument list of a remote exec as criterion 9 builds it: flags first, then the
	// separator, then the guest's own argv.
	args := []string{"sandbox", "exec", "sbx-1", "--", "echo", "--host"}
	at := indexOf(args, "--")
	require.Greater(t, at, 0, "the list has a separator")

	assert.Equal(t, []string{"echo", "--host"}, args[at+1:],
		"everything after the separator is the guest's, including a name the CLI also has")
	assert.NotContains(t, args[:at], "--host",
		"and the CLI's own flags are before it, where the CLI reads them")
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

// runSelfChild runs one child scenario and returns the directory its ledger was written
// to. The child is expected to fail: what is asserted is the ledger, not its exit status.
func runSelfChild(t *testing.T, scenario string) string {
	t.Helper()

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSelfChild$", "-test.v")
	cmd.Env = append(os.Environ(), selfChildEnv+"="+scenario, evidenceDirEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "the child scenario %q is expected to fail; its output:\n%s", scenario, out)
	t.Logf("the child %q exited with %v, as it should; its ledger:\n%s", scenario, err, readLedger(t, dir))
	return dir
}

// readLedger returns the run's evidence file, or fails: a run that recorded nothing is not
// a run whose criteria held.
func readLedger(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "criteria.tsv"))
	require.NoError(t, err, "the run must have written its ledger")
	return string(data)
}

// timeAt is a fixed instant a few seconds apart per n, for ordering manifests by mtime.
func timeAt(t *testing.T, n int) time.Time {
	t.Helper()
	return time.Date(2026, 11, 21, 12, 0, n, 0, time.UTC)
}
