package config

import (
	"os"
	"strings"
	"testing"
)

func TestServerOnlyListensOnLoopback(t *testing.T) {
	p := Paths{Root: "/v"}
	s := State{Port: 8443, BackendPort: 18443}
	if env := VaultwardenEnv(p, s); !strings.Contains(env, "ROCKET_ADDRESS=127.0.0.1\n") {
		t.Fatalf("server not bound to loopback:\n%s", env)
	}
	if cf := Caddyfile(p, s); !strings.Contains(cf, "bind 127.0.0.1") {
		t.Fatalf("caddy not bound to loopback:\n%s", cf)
	}
}

func TestSignupsFollowTheState(t *testing.T) {
	p := Paths{Root: "/v"}
	if env := VaultwardenEnv(p, State{}); !strings.Contains(env, "SIGNUPS_ALLOWED=true\n") {
		t.Fatalf("signups should start open:\n%s", env)
	}
	env := VaultwardenEnv(p, State{SignupsClosed: true})
	if !strings.Contains(env, "SIGNUPS_ALLOWED=false\n") {
		t.Fatalf("signups should close:\n%s", env)
	}
}

func TestDomainMatchesTheClientURL(t *testing.T) {
	env := VaultwardenEnv(Paths{Root: "/v"}, State{Port: 9443})
	if !strings.Contains(env, "DOMAIN=https://localhost:9443\n") {
		t.Fatalf("attachment links need DOMAIN to match the URL:\n%s", env)
	}
}

func TestGeneratedFilesAreOwnerOnly(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if err := Write(p, State{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p.VaultEnv(), p.Caddyfile()} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is %v", f, info.Mode().Perm())
		}
	}
}

func TestLoadBeforeInitSaysWhatToDo(t *testing.T) {
	if _, err := Load(Paths{Root: t.TempDir()}); err != ErrNotSetUp {
		t.Fatalf("got %v", err)
	}
}
