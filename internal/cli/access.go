package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"strings"
	"syscall"
	"time"

	"github.com/mfat/ezVaultwarden/internal/config"
)

const welcome = `Welcome to ezVaultwarden. This wizard will walk you through setting up your own Vaultwarden instance.

Before install, please tell me how you want to access your vault:

1. This computer only
Vaultwarden will be installed on this computer, and it will NOT be accessible from the internet or your local Network.
Note: Uses a self-signed certificate you should accept in your browser

2. Network access via Tailscale
Vaultwarden will be accessible from the internet only by your own devices running Tailscale.
*Requires free Tailscale account

3. Public internet access
Vaultwarden will be available from the public internet using your own domain name. (for example vault.mydomain.com).
*Requires a server accessible from the internet on port 443 and a domain name

`

const (
	minDisk   = 500 << 20
	minMemory = 200 << 20
)

// errChooseAgain sends the user back to the question, with nothing installed.
var errChooseAgain = errors.New("choose again")

// accessPlan is what the user chose, once its checks have passed.
type accessPlan struct {
	mode string
	host string
}

// choosePlan runs before anything is downloaded, so a problem costs nothing. We
// never guess what kind of machine this is: the user picks, and the checks follow
// from that. Scripts name their choice, and --yes alone stays local.
func (a *app) choosePlan(ctx context.Context, f initFlags) (accessPlan, error) {
	if f.access != "" || f.assume {
		mode := f.access
		if mode == "" {
			mode = "local"
		}
		return a.preparePlan(ctx, f, mode)
	}
	for {
		mode, err := a.askAccess()
		if err != nil {
			return accessPlan{}, err
		}
		plan, err := a.preparePlan(ctx, f, mode)
		if errors.Is(err, errChooseAgain) {
			fmt.Fprintln(a.out)
			continue
		}
		return plan, err
	}
}

// askAccess uses the full-screen wizard in a real terminal, and plain prompts
// everywhere else.
func (a *app) askAccess() (string, error) {
	if a.fullScreen != nil {
		return a.fullScreen()
	}
	fmt.Fprint(a.out, welcome)
	for {
		fmt.Fprint(a.out, "Choose 1, 2 or 3: ")
		answer, err := a.line()
		if err != nil {
			return "", errors.New("setup needs an answer. For a script, use: ezvaultwarden init --yes")
		}
		if mode, ok := map[string]string{"1": "local", "2": "tailnet", "3": "public"}[answer]; ok {
			return mode, nil
		}
	}
}

func (a *app) preparePlan(ctx context.Context, f initFlags, mode string) (accessPlan, error) {
	if err := a.checkMachine(f); err != nil {
		return accessPlan{}, err
	}
	switch mode {
	case "local":
		return accessPlan{mode: "local"}, nil
	case "tailnet":
		host, err := a.prepareTailnet(ctx, fmt.Sprintf("http://127.0.0.1:%d", f.port+10000), f)
		return accessPlan{mode: "tailnet", host: host}, err
	case "public":
		host, err := a.preparePublic(ctx, f.domain, f.assume)
		return accessPlan{mode: "public", host: host}, err
	}
	return accessPlan{}, fmt.Errorf("unknown access %q. Choose local, tailnet or public", mode)
}

// checkMachine is every check that applies whatever the user chose.
func (a *app) checkMachine(f initFlags) error {
	fmt.Fprintln(a.out, "\n  Checking this machine...")
	if err := a.rt.Available(); err != nil {
		return err
	}
	disk, memory := a.resources()
	if disk < minDisk {
		return fmt.Errorf("Vaultwarden needs at least 500 MB of free disk space, and there's %d MB", disk>>20)
	}
	if memory < minMemory {
		a.warn("Only %d MB of memory is free, so Vaultwarden may run slowly.", memory>>20)
	}
	for _, port := range []int{f.port, f.port + 10000} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("port %d is in use. Pick another with: ezvaultwarden init --port N", port)
		}
		l.Close()
	}
	a.ok("%d GB of disk and %d MB of memory free, ports are ready", disk>>30, memory>>20)
	return nil
}

// resources returns free disk space under home, and available memory.
func resources() (disk, memory uint64) {
	home, _ := os.UserHomeDir()
	var fs syscall.Statfs_t
	if syscall.Statfs(home, &fs) == nil {
		disk = fs.Bavail * uint64(fs.Bsize)
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return disk, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var kb uint64
		if n, _ := fmt.Sscanf(line, "MemAvailable: %d kB", &kb); n == 1 {
			memory = kb << 10
		}
	}
	return disk, memory
}

// again asks whether to retry after the user fixes something. Scripts can't, so
// they stop with the reason.
func (a *app) again(problem string, scripted bool) error {
	if scripted {
		return errors.New(problem)
	}
	a.warn("%s", problem)
	fmt.Fprint(a.out, "\n  a) Check again\n  b) Choose another option\n\nChoose a or b [a]: ")
	answer, err := a.line()
	if err != nil {
		return err
	}
	if strings.EqualFold(answer, "b") {
		return errChooseAgain
	}
	return nil
}

// prepareTailnet turns Tailscale on before the install, because that's the only
// real proof of permission. It can wait on a server that isn't there yet.
func (a *app) prepareTailnet(ctx context.Context, target string, f initFlags) (string, error) {
	for {
		host, err := a.startServing(ctx, target)
		if err == nil {
			a.ok("Tailscale is connected as %s", host)
			return host, nil
		}
		if err := a.again(err.Error(), f.assume || f.access != ""); err != nil {
			return "", err
		}
	}
}

func (a *app) startServing(ctx context.Context, target string) (string, error) {
	host, err := a.ts.Lookup(ctx)
	if err != nil {
		return "", err
	}
	current, err := a.ts.Proxying(ctx, host)
	if err != nil {
		return "", err
	}
	if current != "" && current != target {
		return "", fmt.Errorf("port 443 on %s is already in use by Tailscale (%s). Free it first", host, current)
	}
	return host, a.ts.Serve(ctx, target)
}

// preparePublic checks the things that would otherwise fail only after the install.
func (a *app) preparePublic(ctx context.Context, host string, scripted bool) (string, error) {
	if host == "" {
		if scripted {
			return "", errors.New("name the vault with --domain vault.example.com")
		}
		fmt.Fprint(a.out, "\nYour domain name, for example vault.mydomain.com: ")
		var err error
		if host, err = a.line(); err != nil {
			return "", err
		}
	}
	host = strings.ToLower(host)
	if !validHost.MatchString(host) {
		return "", fmt.Errorf("%q isn't a domain name. Try something like vault.mydomain.com", host)
	}
	if err := a.confirmReachable(scripted); err != nil {
		return "", err
	}
	for {
		problem := a.portProblem()
		if problem == "" {
			problem = a.dnsProblem(ctx, host)
		}
		if problem == "" {
			break
		}
		if err := a.again(problem, scripted); err != nil {
			return "", err
		}
	}
	a.ok("Port 443 is free, and %s points to this machine", host)
	fmt.Fprintln(a.out, "Continuing accepts Let's Encrypt's terms: https://letsencrypt.org/repository/")
	if !scripted {
		yes, err := a.confirm("Continue?", false)
		if err != nil {
			return "", err
		}
		if !yes {
			return "", errChooseAgain
		}
	}
	return host, nil
}

// confirmReachable warns and never refuses: a cloud server can have a private
// address here and still be reachable, and we can't tell from inside.
func (a *app) confirmReachable(scripted bool) error {
	ip := a.localIP()
	if isPublic(ip) || scripted {
		return nil
	}
	a.warn("This machine's address, %s, looks private, so the internet may not reach it.", ip)
	fmt.Fprintln(a.out, "  Cloud servers can look like this and still work. At home you'd need to forward")
	fmt.Fprintln(a.out, "  port 443 on your router.")
	fmt.Fprint(a.out, "\n  a) Continue anyway\n  b) Choose another option\n\nChoose a or b [b]: ")
	answer, err := a.line()
	if err != nil {
		return err
	}
	if strings.EqualFold(answer, "a") {
		return nil
	}
	return errChooseAgain
}

func (a *app) portProblem() string {
	switch err := a.bind443(); {
	case err == nil:
		return ""
	case errors.Is(err, syscall.EACCES):
		return "Port 443 needs permission once. This lets every program on this machine use it.\n" +
			"  Run this in another terminal, or run ezVaultwarden as root:\n" +
			"    echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/90-ezvaultwarden.conf\n" +
			"    sudo sysctl --system"
	case errors.Is(err, syscall.EADDRINUSE):
		return "Something else on this machine is using port 443. Stop it first."
	default:
		return fmt.Sprintf("Port 443 can't be used: %v", err)
	}
}

func bind443() error {
	l, err := net.Listen("tcp", ":443")
	if err != nil {
		return err
	}
	return l.Close()
}

func (a *app) dnsProblem(ctx context.Context, host string) string {
	ip, err := a.publicIP(ctx)
	if err != nil {
		return fmt.Sprintf("Couldn't find this machine's public address: %v", err)
	}
	found, err := a.resolve(ctx, host)
	if err != nil {
		return fmt.Sprintf("Couldn't look up %s: %v", host, err)
	}
	matched := false
	var others []string
	for _, addr := range found {
		switch {
		case addr == ip:
			matched = true
		case !onThisMachine(addr):
			others = append(others, addr)
		}
	}
	if !matched {
		return fmt.Sprintf("%s doesn't point to this machine yet. At your domain provider, add this record:\n"+
			"    A  %s  %s\n"+
			"  If a proxy is on for this name, such as Cloudflare's orange cloud, turn it off.", host, host, ip)
	}
	if len(others) > 0 {
		return fmt.Sprintf("%s also points to %s, so some devices would go there instead.\n"+
			"  At your domain provider, remove those records, keeping only %s.", host, strings.Join(others, ", "), ip)
	}
	return ""
}

// onThisMachine accepts an IPv6 address the machine really has. publicIP only
// reports the IPv4 one.
func onThisMachine(addr string) bool {
	want := net.ParseIP(addr)
	found, err := net.InterfaceAddrs()
	if want == nil || err != nil {
		return false
	}
	for _, a := range found {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.Equal(want) {
			return true
		}
	}
	return false
}

var outside = &http.Client{Timeout: 10 * time.Second}

// publicIP asks an outside service, only when the user chose public access. The
// address on this machine can be private even on a public server.
func publicIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://1.1.1.1/cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	resp, err := outside.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if ip, ok := strings.CutPrefix(line, "ip="); ok {
			return ip, nil
		}
	}
	return "", errors.New("no answer from the address service")
}

// resolve uses DNS over HTTPS, because many networks intercept or filter ordinary
// DNS and Let's Encrypt sees the public answer anyway.
func resolve(ctx context.Context, host string) ([]string, error) {
	v4, err := resolveType(ctx, host, "A", 1)
	if err != nil {
		return nil, err
	}
	v6, err := resolveType(ctx, host, "AAAA", 28)
	if err != nil {
		return nil, err
	}
	return append(v4, v6...), nil
}

func resolveType(ctx context.Context, host, kind string, code int) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://1.1.1.1/dns-query?type="+kind+"&name="+host, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := outside.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var reply struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	var found []string
	for _, answer := range reply.Answer {
		if answer.Type == code {
			found = append(found, answer.Data)
		}
	}
	return found, nil
}

// confirmAddress proves the chosen address works before asking for an account.
func (a *app) confirmAddress(ctx context.Context, s config.State) error {
	if !a.rt.Running() {
		return nil
	}
	switch s.Mode {
	case "tailnet":
		if err := a.reachable(ctx, s.URL()); err != nil {
			a.warn("It didn't answer at %s yet: %v", s.URL(), err)
			fmt.Fprintln(a.out, "  The first certificate can take a minute. Try the address in a browser.")
			return nil
		}
		a.ok("Available to your devices at %s", s.URL())
	case "public":
		if err := a.reachable(ctx, s.URL()); err != nil {
			return fmt.Errorf("the certificate didn't arrive, so your vault isn't reachable yet: %v\n"+
				"Check that port 443 is open from the internet, then run: ezvaultwarden init", err)
		}
		a.ok("Certificate issued. Your vault is online at %s", s.URL())
	}
	return nil
}

func (a *app) showAccountStep(s config.State) {
	switch s.Mode {
	case "tailnet":
		fmt.Fprintf(a.out, "\nOpen %s and create your account.\nOnly your own devices can reach this.\n", s.URL())
	case "public":
		fmt.Fprintln(a.out, "\nOpen this private link and create your account:")
		fmt.Fprintf(a.out, "  %s/?setup=%s\n", s.URL(), s.SetupToken)
		fmt.Fprintln(a.out, "Only that link can create an account, so keep it to yourself. Signups close after the")
		fmt.Fprintln(a.out, "first one. If you have to stop, nobody else can register. Finish later with: ezvaultwarden init")
	default:
		if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
			fmt.Fprintf(a.out, "\nOpen %s and create your account.\n", s.URL())
		} else {
			fmt.Fprintln(a.out, "\nThis machine has no browser, so open a tunnel from your own computer first:")
			fmt.Fprintf(a.out, "  ssh -L %d:localhost:%d %s\n", s.Port, s.Port, sshTarget())
			fmt.Fprintf(a.out, "Then open %s there and create your account.\n", s.URL())
		}
		fmt.Fprintln(a.out, "Your browser will show a warning. Accept it to continue.")
	}
}

// sshTarget names this machine the way the user reached it, when they came over SSH.
func sshTarget() string {
	name := "you"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host := "this-server"
	if fields := strings.Fields(os.Getenv("SSH_CONNECTION")); len(fields) == 4 {
		host = fields[2]
	}
	return name + "@" + host
}

func (a *app) showPublicSteps() {
	fmt.Fprintln(a.out, "\nProtect your vault now, because anyone can see its login page:")
	fmt.Fprintln(a.out, "  1. Turn on two-step login: in your vault, go to Settings, then Security, then Two-step login")
	fmt.Fprintln(a.out, "  2. Keep backups somewhere else: ezvaultwarden backup --target /path")
	fmt.Fprintln(a.out, "\n  Check signups any time with: ezvaultwarden status")
	fmt.Fprintln(a.out, "  Open or close them with:     ezvaultwarden signups open|close")
}

// signups lets the user close a public vault by hand if they can't finish setup.
func (a *app) signups(args []string) error {
	if len(args) != 1 || (args[0] != "open" && args[0] != "close") {
		return errors.New("choose one: ezvaultwarden signups open|close")
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	s.SignupsClosed = args[0] == "close"
	if err := a.applyMode(context.Background(), s); err != nil {
		return err
	}
	if s.SignupsClosed {
		a.ok("Signups closed, nobody else can register")
		return nil
	}
	a.warn("Signups are open, so anyone who reaches your vault can register. Close them with: ezvaultwarden signups close")
	return nil
}

// exposePublic moves a finished install to the public internet.
func (a *app) exposePublic(ctx context.Context, s config.State, args []string) error {
	fs := flag.NewFlagSet("expose public", flag.ContinueOnError)
	domain := fs.String("domain", "", "name for the vault, such as vault.mydomain.com")
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !s.SignupsClosed {
		return errors.New("signups are still open, so anyone who reaches this could register. Finish with: ezvaultwarden init")
	}
	if err := a.confirmSwitch(s, "public", *assume); err != nil {
		return err
	}
	host, err := a.preparePublic(ctx, *domain, *assume)
	if errors.Is(err, errChooseAgain) {
		return errors.New("nothing changed")
	}
	if err != nil {
		return err
	}
	before := s
	if err := a.closeTailnet(ctx, before); err != nil {
		return err
	}
	s.Mode, s.Hostname = "public", host
	err = a.applyMode(ctx, s)
	if err == nil {
		err = a.confirmAddress(ctx, s)
	}
	if err != nil {
		if undoErr := a.applyMode(ctx, before); undoErr != nil {
			a.warn("Couldn't go back to the previous address: %v", undoErr)
		}
		a.restoreTailnet(ctx, before)
		return err
	}
	a.leaveMode(ctx, before)
	a.warn("Apps set to the old address need the new one. See: ezvaultwarden connect")
	return nil
}

// leaveMode tidies up what the previous mode left outside this folder.
func (a *app) leaveMode(ctx context.Context, before config.State) {
	if before.UsesLego() {
		a.dropDNS(ctx, before)
	}
}

// closeTailnet comes first and must succeed. Tailscale forwards straight to the
// server, past Caddy, so changing our own config wouldn't close it.
func (a *app) closeTailnet(ctx context.Context, s config.State) error {
	if s.Mode != "tailnet" {
		return nil
	}
	if err := a.ts.StopServing(ctx); err != nil {
		return fmt.Errorf("Tailscale can still reach your vault, so nothing changed: %w", err)
	}
	target, err := a.ts.Proxying(ctx, s.Hostname)
	if err != nil {
		return fmt.Errorf("couldn't confirm Tailscale stopped, so nothing changed: %w", err)
	}
	if target != "" {
		return errors.New("Tailscale can still reach your vault, so nothing changed")
	}
	return nil
}

// restoreTailnet puts access back after a failed change.
func (a *app) restoreTailnet(ctx context.Context, s config.State) {
	if s.Mode != "tailnet" {
		return
	}
	if err := a.ts.Serve(ctx, fmt.Sprintf("http://127.0.0.1:%d", s.BackendPort)); err != nil {
		a.warn("Couldn't turn Tailscale back on: %v", err)
	}
}

// tailnetLeak finds Tailscale still forwarding to the vault outside tailnet mode.
func (a *app) tailnetLeak(ctx context.Context, s config.State) bool {
	if s.Mode == "tailnet" {
		return false
	}
	host, err := a.ts.Lookup(ctx)
	if err != nil {
		return false
	}
	target, err := a.ts.Proxying(ctx, host)
	return err == nil && target == fmt.Sprintf("http://127.0.0.1:%d", s.BackendPort)
}
