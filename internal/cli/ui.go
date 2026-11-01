package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/config"
)

// The 16 ANSI colours, so the app follows the terminal's own theme, light or dark,
// and reads the same over SSH.
var (
	uiCyan   = lipgloss.Color("6")
	uiGreen  = lipgloss.Color("2")
	uiYellow = lipgloss.Color("3")
	uiRed    = lipgloss.Color("1")
	uiChip   = lipgloss.Color("8")

	styleBanner = lipgloss.NewStyle().Background(uiCyan).Foreground(lipgloss.Color("0"))
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleHead   = lipgloss.NewStyle().Foreground(uiCyan)
	styleOK     = lipgloss.NewStyle().Foreground(uiGreen)
	styleWarn   = lipgloss.NewStyle().Foreground(uiYellow)
	styleBad    = lipgloss.NewStyle().Foreground(uiRed)
	styleSel    = lipgloss.NewStyle().Background(uiCyan).Foreground(lipgloss.Color("0")).Bold(true)
	styleChip   = lipgloss.NewStyle().Background(uiChip).Foreground(lipgloss.Color("15")).Bold(true)
)

type uiKey struct{ key, label string }

// frame is the layout every screen shares: a banner saying where you are, a
// heading, the body, and the keys along the bottom.
type frame struct {
	crumb, status string
	title, sub    string
	body          []string
	keys          []uiKey
}

func (f frame) render(width, height int) tea.View {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	left := styleBanner.Bold(true).Render(" ezVaultwarden ") + styleBanner.Render(" "+f.crumb)
	right := styleBanner.Render(f.status + " ")
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	banner := left + styleBanner.Render(strings.Repeat(" ", gap)) + right

	lines := []string{banner, "", " " + styleTitle.Render(f.title), " " + styleDim.Render(f.sub),
		" " + styleDim.Render(strings.Repeat("─", max(width-2, 1))), ""}
	room := max(height-len(lines)-2, 1)
	body := f.body
	if len(body) > room {
		body = body[len(body)-room:]
	}
	for _, l := range body {
		lines = append(lines, ansi.Truncate(" "+l, width, "…"))
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	var chips []string
	for _, k := range f.keys {
		chips = append(chips, styleChip.Render(" "+k.key+" ")+" "+k.label)
	}
	lines = append(lines, ansi.Truncate(" "+strings.Join(chips, "  "), width, "…"))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

func selected(text string) string { return styleSel.Render(fmt.Sprintf(" ❯ %-52s", text)) }

func menuRows(items []string, cursor int) []string {
	rows := make([]string, len(items))
	for i, item := range items {
		rows[i] = "   " + item
		if i == cursor {
			rows[i] = selected(item)
		}
	}
	return rows
}

// --- setup, step 1 -------------------------------------------------------------

var setupOptions = []struct{ mode, title, line1, line2 string }{
	{"local", "1  This computer only", "Not reachable from the internet or your network.", "Self-signed certificate, accept it in your browser."},
	{"tailnet", "2  Network access via Tailscale", "Only your own devices running Tailscale.", "Requires a free Tailscale account."},
	{"public", "3  Public internet access", "Your own domain, for example vault.mydomain.com.", "Requires port 443 and a domain name."},
}

type setupModel struct {
	cursor, width, height int
	choice                string
}

func (m setupModel) Init() tea.Cmd { return nil }

func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch key := msg.String(); key {
		case "up", "k":
			m.cursor = (m.cursor + len(setupOptions) - 1) % len(setupOptions)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(setupOptions)
		case "1", "2", "3":
			m.cursor = int(key[0] - '1')
			m.choice = setupOptions[m.cursor].mode
			return m, tea.Quit
		case "enter":
			m.choice = setupOptions[m.cursor].mode
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m setupModel) View() tea.View {
	var body []string
	for i, o := range setupOptions {
		row := "   " + o.title
		if i == m.cursor {
			row = selected(o.title)
		}
		body = append(body, row, styleDim.Render("     "+o.line1), styleDim.Render("     "+o.line2))
		if i < len(setupOptions)-1 {
			body = append(body, "")
		}
	}
	return frame{crumb: "Setup › Step 1 of 5", status: "Not installed yet",
		title: "How do you want to reach your vault?", sub: "You can change this later.", body: body,
		keys: []uiKey{{"↑↓", "choose"}, {"enter", "continue"}, {"q", "quit"}}}.render(m.width, m.height)
}

// pickAccessFullScreen is the wizard's question in a real terminal.
func pickAccessFullScreen() (string, error) {
	result, err := tea.NewProgram(setupModel{}).Run()
	if err != nil {
		return "", err
	}
	if choice := result.(setupModel).choice; choice != "" {
		return choice, nil
	}
	return "", errors.New("nothing was installed")
}

// --- the app -------------------------------------------------------------------

type uiScreen int

const (
	uiHome uiScreen = iota
	uiConnect
	uiBackups
	uiUpdate
	uiAccess
	uiLogs
	uiUninstall
	uiTask
	uiConfirm
	uiInput
)

// uiEvent is one line from a running command, or its end.
type uiEvent struct {
	line string
	done bool
	err  error
}

type uiConfirmation struct {
	title string
	lines []string
	yes   func(m *uiModel) tea.Cmd
}

type uiPrompt struct {
	title, label string
	masked       bool
	value        string
	done         func(m *uiModel, value string) tea.Cmd
}

type uiTaskState struct {
	title    string
	lines    []string
	events   <-chan uiEvent
	finished bool
	failed   bool
	quit     bool // the app has nothing left to show afterwards
}

type uiModel struct {
	a             *app
	s             config.State
	width, height int
	screen        uiScreen
	cursor        int
	note          string

	backups []string
	task    uiTaskState
	check   []string
	confirm uiConfirmation
	prompt  uiPrompt

	logs       []string
	follow     bool
	errorsOnly bool
	search     string
}

type (
	uiEventMsg uiEvent
	uiLogsMsg  []string
	uiTickMsg  struct{}
	uiCheckMsg []string
)

func (a *app) ui([]string) error {
	s, err := a.load()
	if err != nil {
		return err
	}
	m := uiModel{a: a, s: s}
	m.reload()
	_, err = tea.NewProgram(m).Run()
	return err
}

func (m *uiModel) reload() {
	if s, err := m.a.load(); err == nil {
		m.s = s
	}
	m.backups, _ = backup.List(m.a.paths.Backups())
}

func (m uiModel) Init() tea.Cmd { return nil }

// spawnSelf runs this program's own commands, so the app and the command line can
// never disagree. Update replaces the running program, which is safe in a child.
func spawnSelf(stdin string, args ...string) <-chan uiEvent {
	events := make(chan uiEvent, 64)
	go func() {
		defer close(events)
		self, err := os.Executable()
		if err != nil {
			events <- uiEvent{done: true, err: err}
			return
		}
		cmd := exec.Command(self, args...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.StdoutPipe()
		if err != nil {
			events <- uiEvent{done: true, err: err}
			return
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			events <- uiEvent{done: true, err: err}
			return
		}
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			events <- uiEvent{line: lines.Text()}
		}
		events <- uiEvent{done: true, err: cmd.Wait()}
	}()
	return events
}

func nextEvent(events <-chan uiEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return uiEventMsg{done: true}
		}
		return uiEventMsg(event)
	}
}

func (m *uiModel) run(title, stdin string, args ...string) tea.Cmd {
	events := m.a.spawn(stdin, args...)
	m.task = uiTaskState{title: title, events: events}
	m.screen = uiTask
	return nextEvent(events)
}

func (m uiModel) loadLogs() tea.Cmd {
	return func() tea.Msg {
		var buf bytes.Buffer
		_ = m.a.rt.Logs(&buf, 300)
		return uiLogsMsg(strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"))
	}
}

func (m uiModel) checkUpdate() tea.Cmd {
	events := m.a.spawn("", "update", "--check")
	return func() tea.Msg {
		var lines []string
		for event := range events {
			if !event.done {
				lines = append(lines, event.line)
			}
		}
		return uiCheckMsg(lines)
	}
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return uiTickMsg{} })
}

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case uiEventMsg:
		if !msg.done {
			m.task.lines = append(m.task.lines, msg.line)
			return m, nextEvent(m.task.events)
		}
		m.task.finished, m.task.failed = true, msg.err != nil
		m.reload()
	case uiLogsMsg:
		m.logs = msg
	case uiCheckMsg:
		m.check = msg
	case uiTickMsg:
		if m.screen == uiLogs && m.follow {
			return m, tea.Batch(m.loadLogs(), tick())
		}
	case tea.PasteMsg:
		if m.screen == uiInput {
			m.prompt.value += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m uiModel) open(screen uiScreen) (tea.Model, tea.Cmd) {
	m.screen, m.cursor, m.note = screen, 0, ""
	switch screen {
	case uiLogs:
		m.follow = true
		return m, tea.Batch(m.loadLogs(), tick())
	case uiUpdate:
		m.check = nil
		return m, m.checkUpdate()
	}
	return m, nil
}

func (m uiModel) home() uiModel {
	m.screen, m.cursor, m.note = uiHome, 0, ""
	m.reload()
	return m
}

func (m uiModel) homeItems() []string {
	serverAction := "Stop the server"
	if !m.a.rt.Running() {
		serverAction = "Start the server"
	}
	return []string{"Connect an app", "Backups and restore", "Update", "Change access", "Logs", serverAction, "Uninstall"}
}

func (m uiModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.screen {
	case uiInput:
		return m.keyInput(msg)
	case uiConfirm:
		switch key {
		case "y":
			mm := m
			cmd := m.confirm.yes(&mm)
			return mm, cmd
		case "n", "esc":
			return m.home(), nil
		}
		return m, nil
	case uiTask:
		if !m.task.finished {
			return m, nil
		}
		if m.task.quit && !m.task.failed {
			return m, tea.Quit
		}
		return m.home(), nil
	case uiHome:
		return m.keyHome(key)
	case uiConnect:
		return m.keyConnect(key)
	case uiBackups:
		return m.keyBackups(key)
	case uiUpdate:
		switch key {
		case "enter":
			mm := m
			cmd := mm.run("Updating", "", "update", "--yes")
			return mm, cmd
		case "esc", "q":
			return m.home(), nil
		}
		return m, nil
	case uiAccess:
		return m.keyAccess(key)
	case uiLogs:
		return m.keyLogs(key)
	case uiUninstall:
		return m.keyUninstall(key)
	}
	return m, nil
}

func (m uiModel) keyInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.home(), nil
	case "enter":
		mm := m
		cmd := m.prompt.done(&mm, strings.TrimSpace(m.prompt.value))
		return mm, cmd
	case "backspace":
		if v := []rune(m.prompt.value); len(v) > 0 {
			m.prompt.value = string(v[:len(v)-1])
		}
		return m, nil
	}
	m.prompt.value += msg.Key().Text
	return m, nil
}

func (m uiModel) keyHome(key string) (tea.Model, tea.Cmd) {
	items := m.homeItems()
	switch key {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.cursor = (m.cursor + len(items) - 1) % len(items)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(items)
	case "enter":
		switch m.cursor {
		case 0:
			return m.open(uiConnect)
		case 1:
			return m.open(uiBackups)
		case 2:
			return m.open(uiUpdate)
		case 3:
			return m.open(uiAccess)
		case 4:
			return m.open(uiLogs)
		case 5:
			mm := m
			if m.a.rt.Running() {
				return mm, mm.run("Stopping the server", "", "stop")
			}
			return mm, mm.run("Starting the server", "", "start")
		case 6:
			return m.open(uiUninstall)
		}
	}
	return m, nil
}

func (m uiModel) connectCommands() string {
	var b strings.Builder
	if m.s.Mode == "local" {
		fmt.Fprintf(&b, "export NODE_EXTRA_CA_CERTS=%s\n", m.a.paths.RootCert())
	}
	fmt.Fprintf(&b, "bw config server %s\nbw login", m.s.URL())
	return b.String()
}

func (m uiModel) keyConnect(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.note = "Copied the server URL"
		return m, tea.SetClipboard(m.s.URL())
	case "c":
		m.note = "Copied the commands"
		return m, tea.SetClipboard(m.connectCommands())
	case "esc", "q":
		return m.home(), nil
	}
	return m, nil
}

func (m uiModel) keyBackups(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.home(), nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.backups)-1 {
			m.cursor++
		}
	case "n":
		mm := m
		return mm, mm.run("Backing up", "", "backup")
	case "t":
		m.prompt = uiPrompt{title: "Add a backup location", label: "Folder: ", done: func(m *uiModel, path string) tea.Cmd {
			return m.run("Adding a backup location", "", "backup", "--target", path)
		}}
		m.screen = uiInput
	case "enter":
		if len(m.backups) == 0 {
			return m, nil
		}
		file := m.backups[m.cursor]
		m.confirm = uiConfirmation{title: "Restore this backup?", lines: []string{
			filepath.Base(file), "", "This replaces your current vault. A safety copy is kept."},
			yes: func(m *uiModel) tea.Cmd {
				m.prompt = uiPrompt{title: "Restore", label: "Backup key: ", masked: true, done: func(m *uiModel, key string) tea.Cmd {
					return m.run("Restoring", key+"\n", "restore", "--file", file, "--yes")
				}}
				m.screen = uiInput
				return nil
			}}
		m.screen = uiConfirm
	}
	return m, nil
}

var accessChoices = []struct{ mode, label string }{
	{"local", "This computer only"}, {"tailnet", "Tailscale, your devices anywhere"}, {"public", "Public internet"},
}

func (m uiModel) keyAccess(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.home(), nil
	case "up", "k":
		m.cursor = (m.cursor + len(accessChoices) - 1) % len(accessChoices)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(accessChoices)
	case "enter":
		target := accessChoices[m.cursor].mode
		if target == m.s.Mode {
			m.note = "You're already using this."
			return m, nil
		}
		if target == "public" {
			m.prompt = uiPrompt{title: "Public internet access", label: "Your domain name: ", done: func(m *uiModel, host string) tea.Cmd {
				m.askSwitch("public", "--domain", host)
				return nil
			}}
			m.screen = uiInput
			return m, nil
		}
		m.askSwitch(target)
	}
	return m, nil
}

// askSwitch says what's about to change, as the command line does.
func (m *uiModel) askSwitch(target string, extra ...string) {
	lines := []string{"Your account, passwords and backups stay.", ""}
	if m.s.Mode != "local" {
		lines = append(lines, m.s.URL()+" stops working straight away.")
	}
	lines = append(lines, accessEffect(target)...)
	args := append([]string{"expose", target, "--yes"}, extra...)
	m.confirm = uiConfirmation{title: "Change access?", lines: lines, yes: func(m *uiModel) tea.Cmd {
		return m.run("Changing access", "", args...)
	}}
	m.screen = uiConfirm
}

func accessEffect(target string) []string {
	switch target {
	case "tailnet":
		return []string{"Only devices running Tailscale can connect,", "so install it on each one.",
			"Not reachable from the public internet.", styleWarn.Render("⚠") + " This machine's name goes into public certificate logs."}
	case "public":
		return []string{styleWarn.Render("⚠") + " Your login page will be on the public internet,", "  so anyone can try to log in.",
			"Use a strong master password and two-step login."}
	}
	return []string{"Only this machine will be able to connect,", "so your phone and other devices will lose access."}
}

func (m uiModel) keyLogs(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.home(), nil
	case "f":
		m.follow = !m.follow
		if m.follow {
			return m, tea.Batch(m.loadLogs(), tick())
		}
	case "e":
		m.errorsOnly = !m.errorsOnly
	case "/":
		m.prompt = uiPrompt{title: "Search the logs", label: "Show lines with: ", done: func(m *uiModel, text string) tea.Cmd {
			m.search, m.screen = text, uiLogs
			return nil
		}}
		m.screen = uiInput
	}
	return m, nil
}

func (m uiModel) keyUninstall(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.home(), nil
	case "up", "k", "down", "j":
		m.cursor = 1 - m.cursor
	case "enter":
		if m.cursor == 0 {
			m.confirm = uiConfirmation{title: "Remove the service?", lines: []string{"Your vault and backups stay in", m.a.paths.Root}, yes: func(m *uiModel) tea.Cmd {
				cmd := m.run("Uninstalling", "", "uninstall")
				m.task.quit = true
				return cmd
			}}
			m.screen = uiConfirm
			return m, nil
		}
		m.prompt = uiPrompt{title: "Remove everything", label: "Type " + deletePhrase + " to confirm: ", done: func(m *uiModel, typed string) tea.Cmd {
			if !strings.EqualFold(typed, deletePhrase) {
				m.note, m.screen = "Nothing changed.", uiUninstall
				return nil
			}
			cmd := m.run("Deleting your vault", typed+"\n", "uninstall", "--delete-data")
			m.task.quit = true
			return cmd
		}}
		m.screen = uiInput
	}
	return m, nil
}

// --- views ---------------------------------------------------------------------

func (m uiModel) statusLabel() string {
	if m.a.rt.Running() {
		return "● Running"
	}
	return "○ Stopped"
}

func (m uiModel) View() tea.View {
	f := m.view()
	f.status = m.statusLabel()
	if m.note != "" {
		f.body = append(f.body, "", styleOK.Render("✓ "+m.note))
	}
	return f.render(m.width, m.height)
}

func (m uiModel) view() frame {
	back := uiKey{"esc", "back"}
	switch m.screen {
	case uiConnect:
		return frame{crumb: "Home › Connect an app", title: "Connect an app", sub: "Server URL  " + m.s.URL(), body: m.connectBody(),
			keys: []uiKey{{"y", "copy URL"}, {"c", "copy commands"}, back}}
	case uiBackups:
		return frame{crumb: "Home › Backups", title: "Backups", sub: "Encrypted daily. Only your backup key opens them.", body: m.backupBody(),
			keys: []uiKey{{"n", "back up now"}, {"enter", "restore"}, {"t", "add location"}, back}}
	case uiUpdate:
		body := []string{"Checking for updates…"}
		if m.check != nil {
			body = m.check
		}
		return frame{crumb: "Home › Update", title: "Update", sub: "Stops the service for about 20 seconds.",
			body: append(body, "", styleDim.Render("If it isn't healthy, it goes back to your earlier version"), styleDim.Render("and your data from before the update, by itself.")),
			keys: []uiKey{{"enter", "update now"}, back}}
	case uiAccess:
		return m.accessFrame()
	case uiLogs:
		return m.logsFrame()
	case uiUninstall:
		rows := []string{"   Remove the service, keep my vault", styleDim.Render("     Vault and backups stay in"), styleDim.Render("     " + m.a.paths.Root), "",
			"   " + styleBad.Render("Remove everything"), styleDim.Render("     Permanently deletes your vault and every backup."), styleDim.Render("     You'll type ") + deletePhrase + styleDim.Render(" to confirm.")}
		if m.cursor == 0 {
			rows[0] = selected("Remove the service, keep my vault")
		} else {
			rows[4] = selected("Remove everything")
		}
		return frame{crumb: "Home › Uninstall", title: "Uninstall", sub: "Stops the server and removes its services.", body: rows,
			keys: []uiKey{{"↑↓", "choose"}, {"enter", "continue"}, back}}
	case uiTask:
		keys := []uiKey{}
		sub := "Working…"
		if m.task.finished {
			keys, sub = []uiKey{{"any key", "continue"}}, "Finished"
			if m.task.failed {
				sub = styleBad.Render("That didn't work. The details are above.")
			}
		}
		return frame{crumb: "Home", title: m.task.title, sub: sub, body: m.task.lines, keys: keys}
	case uiConfirm:
		return frame{crumb: "Home", title: m.confirm.title, sub: "Nothing has changed yet.", body: m.confirm.lines, keys: []uiKey{{"y", "yes"}, {"n", "no"}}}
	case uiInput:
		value := m.prompt.value
		if m.prompt.masked {
			value = strings.Repeat("*", len([]rune(value)))
		}
		return frame{crumb: "Home", title: m.prompt.title, sub: "Nothing has changed yet.", body: []string{m.prompt.label + value + "█"}, keys: []uiKey{{"enter", "continue"}, {"esc", "cancel"}}}
	}
	return m.homeFrame()
}

func (m uiModel) homeFrame() frame {
	r := m.a.report(m.s)
	body := []string{}
	if r.Backups == 0 {
		body = append(body, styleDim.Render("Backups ")+"  None yet, the first runs tonight")
	} else {
		body = append(body, styleDim.Render("Backups ")+fmt.Sprintf("  Last %s, %d kept", r.LastBackup, r.Backups))
	}
	signups := "signups closed"
	if !r.SignupsClosed || m.a.signupsReopened() {
		signups = styleWarn.Render("signups OPEN")
	}
	body = append(body, styleDim.Render("Accounts")+fmt.Sprintf("  %d, %s", len(r.Accounts), signups))
	if r.Backups > 0 && len(r.BackupTargets) == 0 {
		body = append(body, styleWarn.Render("⚠ Backups are only on this disk."))
	}
	body = append(body, "")
	body = append(body, menuRows(m.homeItems(), m.cursor)...)
	state := styleOK.Render("● Running")
	if !r.Running {
		state = styleBad.Render("○ Stopped")
	}
	return frame{crumb: "Home", title: state + "  " + r.URL, sub: reach(m.s.Mode) + " · Vaultwarden " + r.Version, body: body,
		keys: []uiKey{{"↑↓", "move"}, {"enter", "open"}, {"q", "quit"}}}
}

func (m uiModel) connectBody() []string {
	body := []string{styleHead.Render("Browser extension or desktop app"), "  Settings > Self-hosted > paste the URL above"}
	if m.s.Mode == "local" {
		body = append(body, styleDim.Render("  Your browser warns until you import root.crt."))
	}
	body = append(body, "", styleHead.Render("bw CLI"))
	if m.s.Mode == "local" {
		body = append(body, "  export NODE_EXTRA_CA_CERTS=…/root.crt")
	}
	body = append(body, "  bw config server "+m.s.URL(), "  bw login you@example.com", "", styleHead.Render("Phone"))
	switch m.s.Mode {
	case "tailnet":
		body = append(body, "  Install Tailscale and Bitwarden, then paste the URL above.")
	case "public":
		body = append(body, "  Install Bitwarden, then paste the URL above.")
	case "lan":
		body = append(body, "  Join the same network, then paste the URL above.")
	default:
		body = append(body, styleDim.Render("  Not reachable in local mode.")+" Try Change access.")
	}
	return body
}

func (m uiModel) backupBody() []string {
	var body []string
	if len(m.s.BackupTargets) == 0 {
		body = append(body, styleWarn.Render("⚠ Only on this disk.")+styleDim.Render(" Press ")+"t"+styleDim.Render(" to copy them elsewhere too."), "")
	}
	if len(m.backups) == 0 {
		return append(body, styleDim.Render("None yet. Press ")+"n"+styleDim.Render(" to make one now."))
	}
	body = append(body, styleDim.Render("   When                  Size"))
	const shown = 6
	for i, file := range m.backups {
		if i == shown {
			body = append(body, styleDim.Render(fmt.Sprintf("   … %d more, %d kept", len(m.backups)-shown, len(m.backups))))
			break
		}
		when, size := "", int64(0)
		if info, err := os.Stat(file); err == nil {
			when, size = info.ModTime().Format("2006-01-02 15:04"), info.Size()/1024
		}
		row := fmt.Sprintf("%-20s  %5d KB", when, size)
		if i == m.cursor {
			body = append(body, selected(row))
		} else {
			body = append(body, "   "+row)
		}
	}
	return append(body, "", styleDim.Render(filepath.Base(m.backups[m.cursor])))
}

func (m uiModel) accessFrame() frame {
	var body []string
	for i, c := range accessChoices {
		row := fmt.Sprintf("%-33s", c.label)
		switch {
		case i == m.cursor && c.mode == m.s.Mode:
			row = selected(row + "current")
		case i == m.cursor:
			row = selected(row)
		case c.mode == m.s.Mode:
			row = "   " + row + styleDim.Render("current")
		default:
			row = "   " + row
		}
		body = append(body, row)
	}
	choice := accessChoices[m.cursor]
	body = append(body, "", styleHead.Render(strings.SplitN(choice.label, ",", 2)[0]))
	for _, line := range accessEffect(choice.mode) {
		body = append(body, "  "+line)
	}
	if choice.mode != m.s.Mode {
		body = append(body, styleWarn.Render("⚠")+" Apps set to the old address need the new one.")
	}
	return frame{crumb: "Home › Change access", title: "Change access", sub: "Your account, passwords and backups stay.", body: body,
		keys: []uiKey{{"↑↓", "choose"}, {"enter", "continue"}, {"esc", "back"}}}
}

func (m uiModel) logsFrame() frame {
	var body []string
	for _, line := range m.logs {
		lower := strings.ToLower(line)
		if m.errorsOnly && !strings.Contains(lower, "error") && !strings.Contains(lower, "warn") {
			continue
		}
		if m.search != "" && !strings.Contains(lower, strings.ToLower(m.search)) {
			continue
		}
		body = append(body, line)
	}
	state := "paused, press f to follow"
	if m.follow {
		state = "following, press f to pause"
	}
	body = append(body, "", styleDim.Render("── "+state+" ──"))
	return frame{crumb: "Home › Logs", title: "Logs", sub: "Recent server logs, newest at the bottom.", body: body,
		keys: []uiKey{{"f", "follow"}, {"/", "search"}, {"e", "errors only"}, {"esc", "back"}}}
}

// isTerminal reports whether both ends are a real terminal, which the full-screen
// app needs. Anything else keeps the plain prompts, so scripts and pipes still work.
func isTerminal(in, out *os.File) bool {
	return term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}
