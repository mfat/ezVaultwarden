package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mfat/ezVaultwarden/internal/config"
)

func press(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}

type uiRun struct {
	m     tea.Model
	calls *[][]string
}

func uiSetup(t *testing.T) uiRun {
	t.Helper()
	h, _, _ := planSetup(t, "")
	calls := &[][]string{}
	h.spawn = func(stdin string, args ...string) <-chan uiEvent {
		*calls = append(*calls, append([]string{stdin}, args...))
		events := make(chan uiEvent, 2)
		events <- uiEvent{line: "done: " + strings.Join(args, " ")}
		events <- uiEvent{done: true}
		close(events)
		return events
	}
	s, _ := h.load()
	m := uiModel{a: h.app, s: s, width: 80, height: 24}
	m.reload()
	return uiRun{m: m, calls: calls}
}

func (r *uiRun) send(keys ...string) {
	for _, key := range keys {
		var cmd tea.Cmd
		r.m, cmd = r.m.Update(press(key))
		r.drain(cmd)
	}
}

// drain only follows the task's own events, leaving timers and clipboard alone.
func (r *uiRun) drain(cmd tea.Cmd) {
	m := r.m.(uiModel)
	if cmd == nil || m.screen != uiTask || m.task.finished {
		return
	}
	var next tea.Cmd
	r.m, next = r.m.Update(cmd())
	r.drain(next)
}

func (r *uiRun) screen() string {
	return ansi.Strip(r.m.View().Content)
}

func TestEveryScreenFits80By24(t *testing.T) {
	screens := map[string][]string{
		"home": nil, "connect": {"enter"}, "backups": {"down", "enter"}, "access": {"down", "down", "down", "enter"},
		"uninstall": {"up", "enter"},
	}
	for name, keys := range screens {
		r := uiSetup(t)
		r.send(keys...)
		lines := strings.Split(r.screen(), "\n")
		if len(lines) != 24 {
			t.Errorf("%s: %d lines", name, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > 80 {
				t.Errorf("%s: too wide: %q", name, line)
			}
		}
		if !strings.Contains(lines[0], "ezVaultwarden") || !strings.Contains(lines[0], "Running") {
			t.Errorf("%s: banner %q", name, lines[0])
		}
	}
}

func TestBackupNowRunsTheRealCommand(t *testing.T) {
	r := uiSetup(t)
	r.send("down", "enter", "n")
	if got := (*r.calls)[0]; got[1] != "backup" {
		t.Fatalf("ran %v", got)
	}
	if !strings.Contains(r.screen(), "done: backup") {
		t.Fatalf("no output:\n%s", r.screen())
	}
}

func TestDeletingEverythingNeedsTheExactPhrase(t *testing.T) {
	r := uiSetup(t)
	r.send("up", "enter", "down", "enter")
	for _, c := range "wrong" {
		r.send(string(c))
	}
	r.send("enter")
	if len(*r.calls) != 0 {
		t.Fatalf("ran %v", *r.calls)
	}
	if !strings.Contains(r.screen(), "Nothing changed") {
		t.Fatalf("screen:\n%s", r.screen())
	}
	r.send("enter")
	for _, c := range deletePhrase {
		r.send(string(c))
	}
	r.send("enter")
	if got := (*r.calls)[0]; got[1] != "uninstall" || got[2] != "--delete-data" {
		t.Fatalf("ran %v", got)
	}
}

func TestChangingAccessAsksFirstAndShowsTheEffect(t *testing.T) {
	r := uiSetup(t)
	r.send("down", "down", "down", "enter", "down", "enter")
	if len(*r.calls) != 0 || !strings.Contains(r.screen(), "Only devices running Tailscale") {
		t.Fatalf("calls %v:\n%s", *r.calls, r.screen())
	}
	r.send("y")
	if got := strings.Join((*r.calls)[0][1:], " "); got != "expose tailnet --yes" {
		t.Fatalf("ran %s", got)
	}
}

func TestSayingNoToChangingAccessRunsNothing(t *testing.T) {
	r := uiSetup(t)
	r.send("down", "down", "down", "enter", "down", "enter", "n")
	if len(*r.calls) != 0 || !strings.Contains(r.screen(), "signups") {
		t.Fatalf("calls %v:\n%s", *r.calls, r.screen())
	}
}

func TestConnectShowsNoLocalCertificateStepsWithARealCertificate(t *testing.T) {
	r := uiSetup(t)
	m := r.m.(uiModel)
	m.s = config.State{Mode: "tailnet", Hostname: box}
	body := strings.Join(m.connectBody(), "\n")
	if strings.Contains(body, "root.crt") || !strings.Contains(body, box) {
		t.Fatalf("body: %s", body)
	}
}

func TestSetupStepOneMatchesTheChoices(t *testing.T) {
	var m tea.Model = setupModel{width: 80, height: 24}
	m, _ = m.Update(press("down"))
	m, _ = m.Update(press("enter"))
	if got := m.(setupModel).choice; got != "tailnet" {
		t.Fatalf("chose %q", got)
	}
	m, _ = setupModel{}.Update(press("3"))
	if got := m.(setupModel).choice; got != "public" {
		t.Fatalf("chose %q", got)
	}
	view := ansi.Strip(setupModel{width: 80, height: 24}.View().Content)
	for _, want := range []string{"Setup › Step 1 of 5", "How do you want to reach your vault?", "Requires port 443"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
}

func TestCtrlCCantInterruptARunningCommand(t *testing.T) {
	r := uiSetup(t)
	m := r.m.(uiModel)
	m.screen, m.task = uiTask, uiTaskState{title: "Updating"}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || !strings.Contains(next.(uiModel).note, "Still working") {
		t.Fatalf("cmd %v, note %q", cmd != nil, next.(uiModel).note)
	}
}
