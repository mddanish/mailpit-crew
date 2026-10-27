package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPriority(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	os.WriteFile(file, []byte(`
mode: external
listen: "file:1"
data_dir: /from/file
mailpit:
  api_url: http://file/
  env_file: /opt/mailpit/mailpit.ini
restart:
  mode: command
  command: systemctl restart mailpit
  timeout: 45s
`), 0o600)

	t.Setenv("MPC_LISTEN", "env:2")
	t.Setenv("MPC_DATA_DIR", "/from/env")
	c, err := Load([]string{"--config", file, "--listen", "flag:3"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "flag:3" {
		t.Errorf("flag should win: %s", c.Listen)
	}
	if c.DataDir != "/from/env" {
		t.Errorf("env should beat file: %s", c.DataDir)
	}
	if c.Mailpit.APIURL != "http://file" {
		t.Errorf("file value, trailing slash trimmed: %s", c.Mailpit.APIURL)
	}
	if !reflect.DeepEqual([]string(c.Restart.Command), []string{"systemctl", "restart", "mailpit"}) {
		t.Errorf("command: %q", c.Restart.Command)
	}
	if c.Restart.Timeout.Duration != 45*time.Second {
		t.Errorf("timeout: %s", c.Restart.Timeout)
	}
	if c.UsersFile != "/from/env/users.yaml" {
		t.Errorf("users file default: %s", c.UsersFile)
	}
	if len(c.Mailpit.ManagedDirs) != 1 || c.Mailpit.ManagedDirs[0] != "/opt/mailpit" {
		t.Errorf("managed dirs default: %v", c.Mailpit.ManagedDirs)
	}
	if c.Session.TTL.Duration != 12*time.Hour {
		t.Errorf("default session ttl: %s", c.Session.TTL)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(file, []byte("lisen: x\n"), 0o600)
	if _, err := Load([]string{"--config", file, "--restart-mode", "child"}, io.Discard); err == nil {
		t.Fatal("typo in config accepted")
	}
}

func TestValidation(t *testing.T) {
	if _, err := Load([]string{"--mode", "external", "--restart-mode", "command", "--mailpit-env-file", "/x/mailpit.ini"}, io.Discard); err == nil {
		t.Fatal("command mode without command accepted")
	}
	if _, err := Load([]string{"--mode", "external", "--restart-mode", "magic"}, io.Discard); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := Load([]string{"--mode", "other"}, io.Discard); err == nil {
		t.Fatal("bad crew mode accepted")
	}
	c, err := Load([]string{"--mode", "external", "--restart-mode", "child", "--webroot", "crew"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Webroot != "/crew/" {
		t.Fatalf("webroot normalised: %s", c.Webroot)
	}
}

func TestManagedDefaults(t *testing.T) {
	home := t.TempDir()
	c, err := Load([]string{"--home", home}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "managed" || c.Restart.Mode != "child" {
		t.Fatalf("mode %s restart %s", c.Mode, c.Restart.Mode)
	}
	if c.DataDir != filepath.Join(home, "data") || c.Mailpit.EnvFile != filepath.Join(home, "mailpit", "mailpit.ini") {
		t.Fatalf("paths: %s %s", c.DataDir, c.Mailpit.EnvFile)
	}
	if c.Mailpit.APIURL != "http://127.0.0.1:8026" || c.Mailpit.ManagedDirs[0] != filepath.Join(home, "mailpit") {
		t.Fatalf("api %s dirs %v", c.Mailpit.APIURL, c.Mailpit.ManagedDirs)
	}
	if _, err := Load([]string{"--home", home, "--mailpit-api-listen", "0.0.0.0:8026"}, io.Discard); err == nil {
		t.Fatal("public api listen accepted")
	}
	if _, err := Load([]string{"--home", home, "--mailpit-env-file", "/x"}, io.Discard); err == nil {
		t.Fatal("env file in managed mode accepted")
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := SplitArgs(`docker restart "my mailpit" 'a b' c\ d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "restart", "my mailpit", "a b", "c d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := SplitArgs(`"open`); err == nil {
		t.Fatal("unclosed quote accepted")
	}
}
