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
	// The criteria in these scenarios take no sandbox, so an empty host is enough: what
	// is being checked is the ledger and the run's control flow, and criterion repoints
	// h.t at each subtest itself.
	h := &host{t: t}
	switch os.Getenv(selfChildEnv) {
	case "continues":
		ran := []string{}
		criterion(t, h, "a-fails", "the first criterion fails", func(t *testing.T) {
			ran = append(ran, "a")
			t.Error("this criterion is red")
		})
		criterion(t, h, "b-passes", "the second criterion passes", func(t *testing.T) {
			ran = append(ran, "b")
		})
		criterion(t, h, "c-fails", "the third criterion fails", func(t *testing.T) {
			ran = append(ran, "c")
			assert.Fail(t, "also red")
		})
		// Every body ran, whatever the one before it decided: this is the property, and
		// the parent reads it from the ledger because this process is expected to fail.
		criterion(t, h, "d-ran-all", "all three ran", func(t *testing.T) {
			if strings.Join(ran, ",") != "a,b,c" {
				t.Errorf("only %v ran", ran)
			}
		})
	case "panic":
		criterion(t, h, "panics", "the first criterion panics", func(t *testing.T) {
			panic("a nil map, or worse")
		})
		criterion(t, h, "after", "the next criterion still runs", func(t *testing.T) {
			// Reaching here at all is the property; the ledger records it.
		})
	case "helper":
		// The scenario the first runner hit: a criterion that dies inside a helper rather
		// than at its own assertion. mustRun is a real helper — it asserts on the CLI's
		// exit code through h.t — so this is the same path a criterion takes when a
		// command it requires fails.
		criterion(t, h, "a-helper-fails", "the first criterion fails inside a helper", func(t *testing.T) {
			h.mustRun("definitely-not-a-subcommand")
		})
		criterion(t, h, "b-after-helper", "the next criterion still runs", func(t *testing.T) {
			t.Log("reached")
		})
	case "helper-death":
		// The same, one level colder: the helper fails, and the criterion is recorded
		// FAIL rather than the process ending with no line for it.
		criterion(t, h, "a-dies", "the criterion dies inside a helper", func(t *testing.T) {
			require.Fail(t, "a prerequisite the criterion cannot proceed without")
		})
		criterion(t, h, "b-ran", "the criteria after it ran", func(t *testing.T) {
			t.Log("reached")
		})
	case "notes":
		criterion(t, h, "a-fails", "the first criterion fails after measuring", func(t *testing.T) {
			note(t, "the first criterion measured this")
			t.Error("this criterion is red")
		})
		criterion(t, h, "b-passes", "the second criterion passes after measuring", func(t *testing.T) {
			note(t, "the second criterion measured this")
		})
	case "skipped":
		criterion(t, h, "a-skipped", "a criterion that could not be evaluated", func(t *testing.T) {
			t.Skip("a prerequisite this host does not have")
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
	// The argument list of a remote exec as criterion 9 builds it, through the same
	// function the criterion uses: flags first, then the separator, then the guest's argv.
	// A hand-built list here would pass while `remote()` kept appending after "--", which
	// is what the second host run did.
	args := withCLIFlags([]string{"sandbox", "exec", "sbx-1", "--", "echo", "--host"},
		"--host", "https://127.0.0.1:1")
	at := indexOf(args, "--")
	require.Greater(t, at, 0, "the list has a separator")

	assert.Equal(t, []string{"echo", "--host"}, args[at+1:],
		"everything after the separator is the guest's, including a name the CLI also has")
	assert.Contains(t, args[:at], "--host",
		"and the CLI's own flag is before it, where the CLI reads it")
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
// to. The child's exit status is deliberately not asserted on: a scenario whose criteria
// all skip exits 0 even though its ledger says NOT EVALUATED, and that is exactly the case
// the skip test exists to read. What a run is judged by is the ledger.
func runSelfChild(t *testing.T, scenario string) string {
	t.Helper()

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSelfChild$", "-test.v")
	cmd.Env = append(os.Environ(), selfChildEnv+"="+scenario, evidenceDirEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	t.Logf("the child %q exited with %v; its output:\n%s\nits ledger:\n%s", scenario, err, out, readLedger(t, dir))
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

// TestManifestLacksRootedReadsTheRecordedAgent is the check the older-template search
// rests on: a manifest whose agent does not advertise `rooted` is an older template, and
// one that does is not — the criterion would otherwise pass or fail on the wrong subject.
func TestManifestLacksRootedReadsTheRecordedAgent(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "golden-stock-0.9.0.json")
	require.NoError(t, os.WriteFile(old, []byte(`{
		"name": "stock", "version": "0.9.0",
		"agent": {"agentProtocol": "2", "agentVersion": "src-old", "agentCaps": ["hello", "put", "untar"]}
	}`), 0o644))
	ref, lacks, err := manifestLacksRooted(old)
	require.NoError(t, err)
	assert.True(t, lacks, "a manifest without the capability is an older template")
	assert.Equal(t, "stock@0.9.0", ref)

	current := filepath.Join(dir, "golden-fresh-1.0.0.json")
	require.NoError(t, os.WriteFile(current, []byte(`{
		"name": "fresh", "version": "1.0.0",
		"agent": {"agentProtocol": "2", "agentVersion": "src-new", "agentCaps": ["hello", "put", "rooted"]}
	}`), 0o644))
	_, lacks, err = manifestLacksRooted(current)
	require.NoError(t, err)
	assert.False(t, lacks, "a manifest that advertises it is not the subject")

	// A manifest with no version, or none readable, is not a template this criterion can
	// use: an empty ref would make `inspect` fail for a reason of its own.
	broken := filepath.Join(dir, "golden-broken.json")
	require.NoError(t, os.WriteFile(broken, []byte(`{"name": "broken"}`), 0o644))
	_, _, err = manifestLacksRooted(broken)
	require.Error(t, err, "a manifest that names no version is refused")

	require.Error(t, func() error { _, _, err := manifestLacksRooted(filepath.Join(dir, "absent.json")); return err }())
}

// TestOlderTemplateRefusesAStoreWithNoOlderTemplate pins the failure a host with only
// current templates must produce: the criterion is red with the stores it looked in, not
// silently passed.
func TestOlderTemplateRefusesAStoreWithNoOlderTemplate(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "golden-fresh-1.0.0.json"), []byte(`{
		"name": "fresh", "version": "1.0.0",
		"agent": {"agentCaps": ["hello", "put", "rooted"]}
	}`), 0o644))

	t.Setenv(olderTemplateEnv, "")
	h := &host{t: t, goldenDir: dir}
	_, _, err := h.olderTemplate(t)
	require.Error(t, err, "a host with no older template has not measured the criterion")
	assert.Contains(t, err.Error(), "no template without the rooted capability", "the error says what it looked for")
}

// TestOlderTemplatePrefersTheConfiguredName keeps the operator's choice first, and says so:
// a run whose subject was named must record that, not that it guessed.
func TestOlderTemplatePrefersTheConfiguredName(t *testing.T) {
	t.Setenv(olderTemplateEnv, "stock@0.9.0")
	h := &host{t: t, goldenDir: t.TempDir()}
	ref, source, err := h.olderTemplate(t)
	require.NoError(t, err)
	assert.Equal(t, "stock@0.9.0", ref)
	assert.Contains(t, source, olderTemplateEnv)
}

// TestCriterionSurvivesAFailureInAHelper is the defect the second runner found, and it is
// the one that hides the most: a helper asserts through h.t, so with the *parent* test in
// that field a failure called FailNow on the parent from the criterion's goroutine. The
// binary ended where the run stood, the criteria after it never ran, and the criterion
// that died never reached its own recordStatus — a run that stopped looked exactly like a
// run that recorded nothing.
func TestCriterionSurvivesAFailureInAHelper(t *testing.T) {
	dir := runSelfChild(t, "helper")
	ledger := readLedger(t, dir)

	assert.Contains(t, ledger, "a-helper-fails\t", "the criterion that failed in a helper is recorded")
	assert.Contains(t, ledger, "b-after-helper\t", "and the criterion after it still ran")
	assert.Equal(t, 1, strings.Count(ledger, "\tPASS"), "the one that could run passed:\n%s", ledger)
	assert.Equal(t, 1, strings.Count(ledger, "\tFAIL"), "and the one that died is a FAIL, not a gap:\n%s", ledger)
}

// TestCriterionRecordsItsOwnDeath pins the colder half: the line for a criterion that
// FailNow'd inside itself is written, because the record is deferred and Goexit still runs
// deferred calls.
func TestCriterionRecordsItsOwnDeath(t *testing.T) {
	dir := runSelfChild(t, "helper-death")
	ledger := readLedger(t, dir)

	assert.Contains(t, ledger, "a-dies\t", "a criterion that ended inside itself is still recorded")
	assert.Contains(t, ledger, "b-ran\t", "and the next one ran")
	assert.Equal(t, 1, strings.Count(ledger, "\tFAIL"), "the death is a FAIL line:\n%s", ledger)
}

// TestNotesFollowTheirVerdict pins the shape of a ledger line. A note written when it
// happens sat above a FAIL in the second run's ledger and read like the result —
// run-clean FAIL under a note that said nothing remained — so notes are now emitted after
// the verdict and prefixed by it. Anything else in that column is a note, not a criterion,
// and the runner counts criteria by the first field only.
func TestNotesFollowTheirVerdict(t *testing.T) {
	dir := runSelfChild(t, "notes")
	ledger := readLedger(t, dir)

	lines := strings.Split(strings.TrimSpace(ledger), "\n")
	var verdicts, notes []string
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		require.Len(t, fields, 3, "every ledger line has an id, a time and a status:\n%s", ledger)
		if fields[0] == "" {
			notes = append(notes, fields[2])
			continue
		}
		verdicts = append(verdicts, fields[2])
	}

	assert.Equal(t, []string{"FAIL", "PASS"}, verdicts, "one verdict per criterion, in order:\n%s", ledger)
	assert.Equal(t, []string{"FAIL: the first criterion measured this", "PASS: the second criterion measured this"}, notes,
		"each note is prefixed by the verdict it belongs to:\n%s", ledger)
}

// TestASkipIsNotAPass pins the vocabulary: a criterion that was skipped could not be
// evaluated, and the runner treats exactly PASS, FAIL and NOT EVALUATED. A "SKIP" line
// would be counted as neither, so a run whose every criterion skipped would read green.
func TestASkipIsNotAPass(t *testing.T) {
	dir := runSelfChild(t, "skipped")
	ledger := readLedger(t, dir)

	assert.Contains(t, ledger, "NOT EVALUATED", "a skipped criterion is recorded as not evaluated:\n%s", ledger)
	assert.NotContains(t, ledger, "\tSKIP", "and never as a word the runner does not count:\n%s", ledger)
	assert.NotContains(t, ledger, "\tPASS", "and certainly not as a pass:\n%s", ledger)
}

// TestWithCLIFlagsKeepsTheGuestsArgv is the composition criterion 9 uses, pinned: the
// CLI reads its flags before the separator and the guest reads everything after it.
func TestWithCLIFlagsKeepsTheGuestsArgv(t *testing.T) {
	got := withCLIFlags([]string{"sandbox", "exec", "sbx-1", "--", "/bin/sh", "-c", "echo --host"},
		"--host", "https://127.0.0.1:1", "--ca", "/tmp/ca", "--token-file", "/tmp/t")

	assert.Equal(t, []string{"sandbox", "exec", "sbx-1",
		"--host", "https://127.0.0.1:1", "--ca", "/tmp/ca", "--token-file", "/tmp/t",
		"--", "/bin/sh", "-c", "echo --host"}, got,
		"the CLI's flags go before the separator; the guest's argv stays after it")

	// A call with no separator takes the flags at the end, where the CLI reads them too.
	got = withCLIFlags([]string{"sandbox", "ls"}, "--host", "https://127.0.0.1:1")
	assert.Equal(t, []string{"sandbox", "ls", "--host", "https://127.0.0.1:1"}, got)
}

// TestWithCLIFlagsMovesAFormatFlagOffTheGuestsSide is the third instance of this bug, pinned
// as a property of the composer rather than as one more call site.
//
// refusal() appended "-o json" to a command that already ended in "--" and the guest's argv.
// withCLIFlags only moved the flags it was handed, so the format stayed on the guest's side,
// the CLI answered in prose, and the criterion reported the product refusing something while
// decoding the word "error". A composer that knows the format flag is the CLI's cannot leave
// it there, whichever caller passes it.
func TestWithCLIFlagsMovesAFormatFlagOffTheGuestsSide(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		flag []string
		want []string
	}{
		{
			name: "the refusal that broke run four",
			args: []string{"sandbox", "exec", "sbx-2", "--", "/bin/sh", "-c", "echo elsewhere", "-o", "json"},
			want: []string{"sandbox", "exec", "sbx-2", "-o", "json", "--", "/bin/sh", "-c", "echo elsewhere"},
		},
		{
			name: "a command with no separator keeps its flags together",
			args: []string{"sandbox", "ls", "-o", "json"},
			want: []string{"sandbox", "ls", "-o", "json"},
		},
		{
			name: "the long spelling is the same request",
			args: []string{"sandbox", "exec", "sbx-2", "--", "echo", "--output", "json"},
			want: []string{"sandbox", "exec", "sbx-2", "--output", "json", "--", "echo"},
		},
	} {
		got := withCLIFlags(tc.args)
		assert.Equal(t, tc.want, got, "%s", tc.name)

		// Whatever the caller passed, the format is before the separator and the guest's
		// argv is after it.
		at := indexOf(got, "--")
		if at >= 0 {
			assert.NotContains(t, got[at+1:], "-o", "%s: the guest does not receive the CLI's format", tc.name)
			assert.NotContains(t, got[at+1:], "--output", "%s", tc.name)
		}
	}
}

// TestRunRefusesAFormatFlagOnTheGuestsSide is the door, not the builders: whatever composed
// the command line, a format flag after "--" would answer in prose, so host.run refuses the
// call. host.run consults exactly this function before it starts a process, and the message
// it prints names withCLIFlags — which is why a fourth builder is found by this test rather
// than by a fourth host run.
func TestRunRefusesAFormatFlagOnTheGuestsSide(t *testing.T) {
	for _, bad := range [][]string{
		{"sandbox", "exec", "sbx-1", "--", "echo", "-o", "json"},
		{"sandbox", "exec", "sbx-1", "-o", "json", "--", "echo", "--output"},
	} {
		at, flag := separatorThenFormat(bad)
		require.GreaterOrEqual(t, at, 0, "%v carries a format flag on the wrong side", bad)
		assert.Contains(t, []string{"-o", "--output"}, flag)
	}

	for _, good := range [][]string{
		{"sandbox", "ls"},
		withCLIFlags([]string{"sandbox", "exec", "sbx-1", "--", "echo"}, "-o", "json"),
		withCLIFlags([]string{"sandbox", "exec", "sbx-1", "--", "echo", "-o", "json"}),
	} {
		at, _ := separatorThenFormat(good)
		assert.Equal(t, -1, at, "%v is composed correctly", good)
	}
}

