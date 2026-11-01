package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestAdminTokenHashVerifiesAndHidesThePassword(t *testing.T) {
	phc, err := hashAdminToken("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		t.Fatalf("not a PHC string: %s", phc)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		t.Fatal(err)
	}
	if got := argon2.IDKey([]byte("hunter2"), salt, argonTime, argonMemory, argonThread, 32); !bytes.Equal(got, want) {
		t.Fatal("hash doesn't verify")
	}
	if strings.Contains(phc, "hunter2") {
		t.Fatal("password in the hash")
	}
}

func TestAdminPageOpensThenClosesWithoutLeavingATraceOfThePassword(t *testing.T) {
	h := setup(t, "\n")
	if err := h.admin(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "/admin") {
		t.Fatalf("no address:\n%s", h.output)
	}
	if h.adminLeftOpen() {
		t.Fatal("admin page still configured")
	}
	if !h.rt.running {
		t.Fatal("server left down")
	}
	if !strings.Contains(h.output.String(), "Admin page closed") {
		t.Fatalf("no confirmation:\n%s", h.output)
	}
}

func TestAdminCloseRecoversAKilledSession(t *testing.T) {
	h := setup(t, "")
	s, _ := h.load()
	s.AdminToken = "$argon2id$left-behind"
	if err := h.rewrite(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if !h.adminLeftOpen() {
		t.Fatal("setup didn't open it")
	}
	env, _ := os.ReadFile(h.paths.VaultEnv())
	if !strings.Contains(string(env), "ADMIN_TOKEN='$argon2id$left-behind'") {
		t.Fatalf("the hash must be quoted:\n%s", env)
	}
	if err := h.admin([]string{"--close"}); err != nil {
		t.Fatal(err)
	}
	if h.adminLeftOpen() {
		t.Fatal("still open")
	}
}

func TestSavedAdminSettingsThatUndoOursAreCaught(t *testing.T) {
	h := setup(t, "")
	s, _ := h.load()
	body := `{"signups_allowed":true,"domain":"https://elsewhere.example","admin_token":"x"}`
	if err := os.WriteFile(filepath.Join(h.paths.Data(), "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(h.overrides(s), "\n")
	for _, want := range []string{"Signups are OPEN", "elsewhere.example", "stays open"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "Signups are OPEN") {
		t.Fatalf("status hides it:\n%s", h.output)
	}
	if strings.Contains(h.output.String(), "signups closed") {
		t.Fatalf("status contradicts itself:\n%s", h.output)
	}
}

func TestHarmlessSavedSettingsStayQuiet(t *testing.T) {
	h := setup(t, "")
	s, _ := h.load()
	body := fmt.Sprintf(`{"signups_allowed":false,"domain":%q,"invitations_allowed":false}`, s.URL())
	if err := os.WriteFile(filepath.Join(h.paths.Data(), "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.overrides(s); len(got) != 0 {
		t.Fatalf("false alarm: %v", got)
	}
}
