package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPriority(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	os.WriteFile(file, []byte(`
home: /from/file
listen: "[::]:1001"
data_dir: /data/file
managed:
  version: latest
restart:
  timeout: 45s
`), 0o600)

	t.Setenv("MPC_LISTEN", "[::]:1002")
	t.Setenv("MPC_DATA_DIR", "/data/env")
	c, err := Load([]string{"--config", file, "--listen", "[::]:1003"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "[::]:1003" || c.ListenPort() != "1003" {
		t.Errorf("flag should win: %s", c.Listen)
	}
	if c.DataDir != "/data/env" {
		t.Errorf("env should beat file: %s", c.DataDir)
	}
	if c.Home != "/from/file" || c.MailpitDir() != "/from/file/mailpit" {
		t.Errorf("home %s", c.Home)
	}
	if c.Managed.Version != "latest" || c.Restart.Timeout.Duration != 45*time.Second {
		t.Errorf("file values: %+v", c.Managed)
	}
	if c.UsersFile != "/data/env/users.yaml" {
		t.Errorf("users file default: %s", c.UsersFile)
	}
}

func TestDefaults(t *testing.T) {
	home := t.TempDir()
	c, err := Load([]string{"--home", home}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "[::]:8080" || c.DataDir != filepath.Join(home, "data") || c.Webroot != "/" {
		t.Fatalf("defaults: %s %s %s", c.Listen, c.DataDir, c.Webroot)
	}
}

func TestValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(file, []byte("lisen: x\n"), 0o600)
	if _, err := Load([]string{"--config", file}, io.Discard); err == nil {
		t.Fatal("typo in config accepted")
	}
	os.WriteFile(file, []byte("mode: external\n"), 0o600)
	if _, err := Load([]string{"--config", file}, io.Discard); err == nil {
		t.Fatal("external mode accepted")
	}
	if _, err := Load([]string{"--listen", "8080"}, io.Discard); err == nil {
		t.Fatal("listen without host accepted")
	}
	c, err := Load([]string{"--webroot", "crew"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Webroot != "/crew/" {
		t.Fatalf("webroot normalised: %s", c.Webroot)
	}
}
