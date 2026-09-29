package app

// In-app daed log viewer. The first version shelled out via tea.ExecProcess,
// which took over the whole terminal: the TUI vanished, nothing said how to
// get back, and mouse reporting died with the handoff. Here journalctl runs
// as an ordinary child process whose output streams line by line into an
// overlay — scrolling, pausing the follow and closing all stay inside the app.

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

const (
	// How many history lines journalctl is asked for on open/reload.
	logsHistoryLines = 300
	// Ring cap for the buffer: beyond it the head is dropped, so a long
	// session cannot grow without bound.
	logsMaxBuffer = 2000
	// Lines the reader may buffer ahead of the UI chain.
	logsChanBuffer = 256
)

type logsViewer struct {
	open      bool
	following bool // a follower process is streaming
	loading   bool // a spawn is in flight
	stopped   bool // the follower exited on its own (unit gone, journal error)
	// wantFollow is the user's latest follow intent. A cancel that lands
	// while a spawn is still in flight must win over that spawn: without
	// it, the late logsSpawnMsg would flip following back on and the new
	// follower would start streaming over the pause.
	wantFollow bool
	err        string
	lines      []string
	scroll     int // index of the first visible line
	proc       *logsProc
}

// logsProc is one journalctl child. lines streams its merged stdout/stderr
// and is closed by the reader once the child has fully exited; kill is
// idempotent and terminates the child (journalctl -f only exits on a signal).
type logsProc struct {
	lines <-chan string
	kill  func()
}

// startLogsProc is a var so headless tests can swap the real journalctl for
// a fake feeder — a real -f child would never exit inside `go test`.
var startLogsProc = realStartLogsProc

func realStartLogsProc(tail int) (*logsProc, error) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return nil, errors.New(i18n.T("未找到 journalctl（daed 需以 systemd 服务运行在本机）"))
	}
	c := exec.Command("journalctl", "-u", "daed", "-n", strconv.Itoa(tail), "--no-pager", "-f")

	// One pipe for both streams: journalctl's hints ("-- No entries --",
	// permission notices) land in stderr and are part of what the user
	// needs to see when the log looks empty.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close() // the child owns the write end now

	lines := make(chan string, logsChanBuffer)
	done := make(chan struct{})
	var once sync.Once
	p := &logsProc{
		lines: lines,
		kill: func() {
			once.Do(func() {
				close(done)
				if c.Process != nil {
					_ = c.Process.Signal(syscall.SIGTERM)
				}
			})
		},
	}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024) // log lines run long
	read:
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-done:
				break read
			}
		}
		// Drain until EOF so a signalled child blocked on a full pipe can
		// finish dying, then reap it; closing lines ends the UI-side chain.
		_, _ = io.Copy(io.Discard, pr)
		pr.Close()
		_ = c.Wait()
		close(lines)
	}()
	return p, nil
}

type logsSpawnMsg struct {
	proc  *logsProc
	err   error
	fresh bool // true: replace the buffer (open/reload); false: resume follow
}

type logsLineMsg struct {
	lines <-chan string
	line  string
}

// logsStoppedMsg carries its stream so a stop echoed from a chain that was
// already replaced (r reload, f pause/resume) cannot be mistaken for the
// current follower dying.
type logsStoppedMsg struct {
	lines <-chan string
}

// logsSpawnCmd starts a follower; tail 0 means "from now on" (resume), any
// larger value replays that much history first.
func logsSpawnCmd(tail int, fresh bool) tea.Cmd {
	return func() tea.Msg {
		p, err := startLogsProc(tail)
		return logsSpawnMsg{proc: p, err: err, fresh: fresh}
	}
}

// logsWaitCmd is the streaming chain link: it blocks until the next line (or
// the stream's end) and returns it as a message; the handler re-arms it.
func logsWaitCmd(lines <-chan string) tea.Cmd {
	return func() tea.Msg {
		if line, ok := <-lines; ok {
			return logsLineMsg{lines: lines, line: line}
		}
		return logsStoppedMsg{lines: lines}
	}
}

func (v *logsViewer) kill() {
	if v.proc != nil {
		v.proc.kill()
	}
}

// appendLine adds one line, trimming the buffer head past the cap. scroll is
// shifted along with the drop so a scrolled-up view does not jump.
func (v *logsViewer) appendLine(line string) {
	v.lines = append(v.lines, line)
	if over := len(v.lines) - logsMaxBuffer; over > 0 {
		v.lines = v.lines[over:]
		if v.scroll > over {
			v.scroll -= over
		} else {
			v.scroll = 0
		}
	}
}

func logsWinBody(avail int) int {
	n := avail - 6 // box borders, title, footer and breathing room
	if n < 4 {
		n = 4
	}
	return n
}

func logsMaxScroll(n, win int) int {
	if s := n - win; s > 0 {
		return s
	}
	return 0
}

func clampLogsScroll(scroll, n, win int) int {
	if scroll > logsMaxScroll(n, win) {
		scroll = logsMaxScroll(n, win)
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// logsOverlayBox renders the viewer as a wide floating window over any page.
// Log lines are head-truncated: the tail of the line is the message, the
// head is only the timestamp.
func logsOverlayBox(w, avail int, v *logsViewer) string {
	win := logsWinBody(avail)
	scroll := clampLogsScroll(v.scroll, len(v.lines), win)
	end := scroll + win
	if end > len(v.lines) {
		end = len(v.lines)
	}
	boxW := w - 4
	if boxW > 140 {
		boxW = 140
	}
	inner := boxW - 6

	var body []string
	switch {
	case v.err != "":
		body = append(body, " "+ui.ErrorStyle.Render(ui.TruncateHead(v.err, inner-1)))
	case len(v.lines) == 0:
		hint := i18n.T("（暂无输出）")
		if v.loading {
			hint = i18n.T("（加载中…）")
		}
		body = append(body, " "+ui.HelpStyle.Render(hint))
	default:
		for _, l := range v.lines[scroll:end] {
			body = append(body, " "+ui.TruncateHead(l, inner-1))
		}
	}
	for len(body) < win {
		body = append(body, "")
	}

	// Footer state: following / paused-by-user / exited-on-its-own.
	state, fKey := i18n.T("跟随已暂停"), i18n.T("f 继续")
	if v.following || v.loading {
		state, fKey = i18n.T("跟随中"), i18n.T("f 暂停")
	} else if v.stopped {
		state = i18n.T("已退出")
	}
	footer := state + " · " + i18n.T("%d-%d / %d", scroll+1, end, len(v.lines)) +
		" · " + fKey + " · " + i18n.T("r 重载") + " · " + i18n.T("j/k 滚动") + " · " + i18n.T("q 关闭")
	return strings.Join(ui.TitledBoxFooter(i18n.T(" daed 日志"), footer, true, boxW, body), "\n")
}
