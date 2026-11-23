package agent

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCodexHandshake is the initialize/thread/turn preamble shared by the
// idle-timer fixtures below.
func fakeCodexHandshake(thread, turn string) string {
	return "" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'` + "\n" +
		`read line` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"` + thread + `"}}}'` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":3,"result":{}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"` + thread + `","turn":{"id":"` + turn + `"}}}'` + "\n"
}

// A Codex app-server that goes silent with no tool in flight is hung
// (mdostal/pantheon-v2#538, PANT-1275). The tool-aware idle timer must end the
// turn well before the long semantic timer, and report it as semantic
// inactivity so the daemon classifies it as retryable.
func TestCodexIdleTimeoutEndsSilentTurnWithNoToolInFlight(t *testing.T) {
	// Not t.Parallel(): this test mutates codexGracefulShutdownTimeoutNanos.
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	codexGracefulShutdownTimeoutNanos.Store(int64(100 * time.Millisecond))
	t.Cleanup(func() { codexGracefulShutdownTimeoutNanos.Store(0) })

	fakePath := writeFakeCodexAppServer(t, fakeCodexHandshake("thr-idle", "turn-idle")+
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-idle","item":{"type":"agentMessage","id":"msg-1","text":"starting"}}}'`+"\n"+
		`sleep 3`+"\n")

	started := time.Now()
	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   8 * time.Second,
		SemanticInactivityTimeout: 6 * time.Second,
		CodexIdleTimeout:          200 * time.Millisecond,
	})
	if result.Status != "timeout" {
		t.Fatalf("expected timeout, got status=%q error=%q", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, CodexSemanticInactivityMarker) {
		t.Fatalf("expected %q marker (retryable classification), got %q", CodexSemanticInactivityMarker, result.Error)
	}
	if !strings.Contains(result.Error, "after 200ms") {
		t.Fatalf("expected the idle window in the error, got %q", result.Error)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("idle timer took %s; the 6s semantic timer must not be what fired", elapsed)
	}
}

// A long-running tool emits nothing until it finishes. That is a slow run,
// not a hung one: the idle timer must stay suspended while a tool is in flight.
func TestCodexIdleTimeoutSparesSilentToolInFlight(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := writeFakeCodexAppServer(t, fakeCodexHandshake("thr-tool", "turn-tool")+
		`echo '{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"thr-tool","item":{"type":"commandExecution","id":"cmd-1","command":"make test"}}}'`+"\n"+
		`sleep 0.6`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-tool","item":{"type":"commandExecution","id":"cmd-1","command":"make test","aggregatedOutput":"ok","exitCode":0}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-tool","item":{"type":"agentMessage","id":"msg-1","text":"tests pass"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-tool","turn":{"id":"turn-tool","status":"completed"}}}'`+"\n")

	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   10 * time.Second,
		SemanticInactivityTimeout: 5 * time.Second,
		CodexIdleTimeout:          150 * time.Millisecond,
	})
	if result.Status != "completed" {
		t.Fatalf("expected completed, got status=%q error=%q", result.Status, result.Error)
	}
}

func TestCodexIdleTrackerCountsToolsAndClampsAtZero(t *testing.T) {
	tr := newCodexIdleTracker(time.Hour)
	defer tr.stop()
	tr.observe("tool-result:exec_command") // dropped tool-use: must not go negative
	if got := tr.inFlight(); got != 0 {
		t.Fatalf("inFlight after unmatched result = %d, want 0", got)
	}
	tr.observe("tool-use:exec_command")
	tr.observe("tool-use:patch_apply")
	tr.observe("text")
	if got := tr.inFlight(); got != 2 {
		t.Fatalf("inFlight = %d, want 2", got)
	}
	tr.observe("tool-result:exec_command")
	tr.observe("tool-result:patch_apply")
	if got := tr.inFlight(); got != 0 {
		t.Fatalf("inFlight = %d, want 0", got)
	}

	off := newCodexIdleTracker(0)
	if off.C() != nil {
		t.Fatal("zero timeout must disable the idle timer")
	}
	off.observe("tool-use:x")
	off.rearm()
	off.stop()
}
