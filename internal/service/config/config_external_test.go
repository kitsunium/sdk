package config_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
)

type appConf struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

// Validate implements core/config.Validator.
func (c appConf) Validate() error {
	//: a zero port is invalid.
	if c.Port == 0 {
		//: surface a plain error; Load relabels it CONFIG_VALIDATION_FAILED.
		return errs.NewRuntime(coreconfig.CodeConfigValidationFailed, "PORT_REQUIRED", "port required", "test: port is zero")
	}
	//: otherwise valid.
	return nil
}

// TestEnvSource reads prefixed env vars into a flat map.
func TestEnvSource(t *testing.T) {
	t.Setenv("APP_PORT", "8080")
	t.Setenv("OTHER_X", "ignored")
	m, err := cfg.EnvSource("APP").Load()
	//: the load never errors.
	if err != nil {
		t.Fatalf("EnvSource.Load: %v", err)
	}
	//: the prefixed key is stripped, lower-cased, and coerced to a number.
	if m["port"] != float64(8080) {
		t.Errorf("port=%v, want 8080", m["port"])
	}
	//: a non-prefixed var is excluded.
	if _, ok := m["x"]; ok {
		t.Error("OTHER_X leaked into the APP namespace")
	}
}

// TestLoadMergeValidate merges file + env (env wins) and validates.
func TestLoadMergeValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	//: the file sets host + a port that env will override.
	if err := os.WriteFile(path, []byte(`{"port":1,"host":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PORT", "9090")
	var conf appConf
	//: env is listed after the file, so its port wins.
	err := cfg.Load(&conf, cfg.FileSource("json", path), cfg.EnvSource("APP"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	//: env overrode the file port.
	if conf.Port != 9090 {
		t.Errorf("Port=%d, want 9090 (env override)", conf.Port)
	}
	//: the file-only field survived.
	if conf.Host != "file" {
		t.Errorf("Host=%q, want file", conf.Host)
	}
}

// TestValidationFailure surfaces CONFIG_VALIDATION_FAILED.
func TestValidationFailure(t *testing.T) {
	t.Parallel()
	var conf appConf // Port stays 0 → Validate fails
	err := cfg.Load(&conf, cfg.EnvSource("MISSING"))
	//: a zero-port config fails validation with the typed code.
	if !errs.HasCode(err, coreconfig.CodeConfigValidationFailed) {
		t.Errorf("err=%v, want CONFIG_VALIDATION_FAILED", err)
	}
}

// TestPollWatcher detects a file change and fires onChange.
func TestPollWatcher(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "watched.json")
	//: seed the file so the initial fingerprint succeeds.
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := cfg.PollWatcher(path, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	var watchErr error
	//: Watch blocks until cancel; wg.Wait joins it at the end.
	wg.Go(func() {
		watchErr = w.Watch(ctx, func() {
			//: non-blocking signal so the watcher never stalls.
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	})
	//: mutate the file so mtime/size changes.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"v":222}`), 0o600); err != nil {
		t.Fatal(err)
	}
	//: expect a change notification within a generous window.
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("poll watcher did not detect the change")
	}
	cancel()
	wg.Wait()
	//: a clean ctx-cancel stop returns nil.
	if watchErr != nil {
		t.Errorf("Watch returned %v, want nil on cancel", watchErr)
	}
}
