package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeLogs swaps the journalctl child for a hand-fed channel so the streaming
// chain can be exercised headless: send on lines to produce a log line, and
// kill (q, f pause, r reload) closes the stream exactly like the real reader
// goroutine does once the child exits. Every spawn gets a fresh channel.
type fakeLogs struct {
	lines  chan string
	tails  []int // tail requested by every spawn, in order
	killed int
}

func withFakeLogs(t *testing.T) *fakeLogs {
	t.Helper()
	f := &fakeLogs{}
	orig := startLogsProc
	startLogsProc = func(tail int) (*logsProc, error) {
		f.tails = append(f.tails, tail)
		ch := make(chan string, 512)
		f.lines = ch
		var once bool
		return &logsProc{
			lines: ch,
			kill: func() {
				if !once {
					once = true
					f.killed++
					close(ch) // the real reader closes the stream on child exit
				}
			},
		}, nil
	}
	t.Cleanup(func() { startLogsProc = orig })
	return f
}

// feedLine pushes one line through the wait chain: the pending cmd picks it
// off the channel, the handler appends it and hands back the next link.
func feedLine(t *testing.T, f *fakeLogs, m tea.Model, wait tea.Cmd, line string) (tea.Model, tea.Cmd) {
	t.Helper()
	f.lines <- fmt.Sprintf("Sep 28 10:00:00 host daed[1]: %s", line)
	msg := wait()
	lm, ok := msg.(logsLineMsg)
	if !ok {
		t.Fatalf("wait cmd msg = %T(%+v), want logsLineMsg", msg, msg)
	}
	return m.Update(lm)
}

// TestLogsOverlayStreams the journalctl output into an in-app window: L opens
// it without leaving the TUI, new lines stick to the bottom while following,
// scrolling up detaches the view (new lines wait below), and q closes and
// kills the child.
func TestLogsOverlayStreams(t *testing.T) {
	f := withFakeLogs(t)
	m := newTestModel(t)

	m, cmdL := m.Update(key("L"))
	if cmdL == nil {
		t.Fatal("L should spawn the log viewer")
	}
	spawn, ok := cmdL().(logsSpawnMsg)
	if !ok || spawn.err != nil || !spawn.fresh {
		t.Fatalf("L cmd = %T(%+v), want a fresh logsSpawnMsg", cmdL(), cmdL())
	}
	m, wait := m.Update(spawn)
	mm := m.(Model)
	if !mm.logs.open || !mm.logs.following {
		t.Fatal("the viewer should be open and following")
	}
	if got := f.tails[0]; got != logsHistoryLines {
		t.Fatalf("open should ask for history, got tail %d", got)
	}
	if v := mm.View(); !strings.Contains(v, " daed 日志") {
		t.Fatalf("the log overlay should be visible:\n%s", v)
	}

	// Fill past one window (win = 24 at 36 rows) and check the bottom pin.
	for i := 0; i < 30; i++ {
		m, wait = feedLine(t, f, m, wait, fmt.Sprintf("line-%02d", i))
	}
	v := m.(Model).View()
	if !strings.Contains(v, "line-29") || strings.Contains(v, "line-00") {
		t.Fatalf("a following view should sit on the tail:\n%s", v)
	}

	// Scrolling up detaches: the next streamed line waits below the fold.
	m, _ = m.Update(key("k"))
	if v := m.(Model).View(); !strings.Contains(v, "line-03") {
		t.Fatalf("k should scroll up into history:\n%s", v)
	}
	m, wait = feedLine(t, f, m, wait, "tail-new")
	if v := m.(Model).View(); strings.Contains(v, "tail-new") {
		t.Fatalf("a detached view must not jump to new lines:\n%s", v)
	}

	// G re-pins to the bottom, where the new line has been waiting.
	m, _ = m.Update(key("G"))
	if v := m.(Model).View(); !strings.Contains(v, "tail-new") {
		t.Fatalf("G should jump back to the tail:\n%s", v)
	}

	// f pauses the follow and kills the child; f again resumes from now on
	// (tail 0) without wiping the buffer.
	m, _ = m.Update(key("f"))
	mm = m.(Model)
	if mm.logs.following || f.killed != 1 {
		t.Fatalf("f should pause and kill the follower: following=%v killed=%d", mm.logs.following, f.killed)
	}
	m, cmdF := m.Update(key("f"))
	spawn, ok = cmdF().(logsSpawnMsg)
	if !ok || spawn.err != nil || spawn.fresh {
		t.Fatalf("resume cmd = %T(%+v), want a non-fresh spawn", cmdF(), cmdF())
	}
	if got := f.tails[len(f.tails)-1]; got != 0 {
		t.Fatalf("resume should ask for no history, got tail %d", got)
	}
	m, wait = m.Update(spawn)
	if v := m.(Model).View(); !strings.Contains(v, "line-29") {
		t.Fatalf("resuming the follow must keep the buffered history:\n%s", v)
	}
	m, wait = feedLine(t, f, m, wait, "resumed-1")
	if v := m.(Model).View(); !strings.Contains(v, "resumed-1") {
		t.Fatalf("resumed stream should append and pin:\n%s", v)
	}

	// q closes the window and kills the child; the page keys work again.
	m, _ = m.Update(key("q"))
	mm = m.(Model)
	if mm.logs.open || f.killed != 2 {
		t.Fatalf("q should close and kill: open=%v killed=%d", mm.logs.open, f.killed)
	}
	if v := mm.View(); strings.Contains(v, " daed 日志") {
		t.Fatalf("the overlay should be gone:\n%s", v)
	}
	m2, _ := mm.Update(key("2"))
	if m2.(Model).page != pageTree {
		t.Fatal("after closing the logs page keys should reach the app again")
	}
}

// TestLogsReloadRestartsFollower: r tears the follower down and asks for the
// history again — the buffer is replaced, not appended to.
func TestLogsReloadRestartsFollower(t *testing.T) {
	f := withFakeLogs(t)
	m := newTestModel(t)

	m, cmdL := m.Update(key("L"))
	m, wait := m.Update(cmdL().(logsSpawnMsg))
	m, wait = feedLine(t, f, m, wait, "old-line")
	_ = wait

	m, cmdR := m.Update(key("r"))
	if cmdR == nil {
		t.Fatal("r should restart the follower")
	}
	mm := m.(Model)
	if !mm.logs.loading {
		t.Fatal("r should mark the reload in flight")
	}
	if f.killed == 0 {
		t.Fatal("r should kill the previous follower first")
	}
	spawn, ok := cmdR().(logsSpawnMsg)
	if !ok || spawn.err != nil || !spawn.fresh {
		t.Fatalf("r cmd = %T, want a fresh spawn", cmdR())
	}
	m, wait = m.Update(spawn)
	if v := m.(Model).View(); strings.Contains(v, "old-line") {
		t.Fatalf("a reload replaces the buffer:\n%s", v)
	}
	m, wait = feedLine(t, f, m, wait, "new-line")
	if v := m.(Model).View(); !strings.Contains(v, "new-line") {
		t.Fatalf("the reloaded stream should feed through:\n%s", v)
	}
	_ = wait
}

// TestLogsSpawnErrorToasts: a failed spawn (journalctl missing, spawn error)
// never opens the window; the failure surfaces as a toast.
func TestLogsSpawnErrorToasts(t *testing.T) {
	orig := startLogsProc
	startLogsProc = func(int) (*logsProc, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { startLogsProc = orig })

	m := newTestModel(t)
	m, cmdL := m.Update(key("L"))
	m, _ = m.Update(cmdL())
	mm := m.(Model)
	if mm.logs.open {
		t.Fatal("a failed spawn must not open the viewer")
	}
	if v := mm.View(); !strings.Contains(v, "✗ 打开日志") {
		t.Fatalf("the failure should toast:\n%s", v)
	}
}
