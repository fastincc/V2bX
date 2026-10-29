package cmd

import (
	"errors"
	"github.com/InazumaV/V2bX/conf"
	core "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/node"
	"os"
	"path/filepath"
	"testing"
)

type reloadCore struct {
	core.Core
	fail    bool
	started bool
}

func (c *reloadCore) Start() error {
	if c.fail {
		return errors.New("injected start failure")
	}
	c.started = true
	return nil
}
func (c *reloadCore) Close() error { c.started = false; return nil }
func TestCandidateFailureRestoresOldRuntime(t *testing.T) {
	core.RegisterCore("reload-test", func(c *conf.CoreConfig) (core.Core, error) { return &reloadCore{fail: c.Name == "fail"}, nil })
	old := &conf.Conf{CoresConfig: []conf.CoreConfig{{Type: "reload-test"}}}
	backup, e := backupConfig(old)
	if e != nil {
		t.Fatal(e)
	}
	defer backup.Close()
	vc, e := newCoreSafely(old)
	if e != nil {
		t.Fatal(e)
	}
	vc.Start()
	nodes := node.New()
	bad := &conf.Conf{CoresConfig: []conf.CoreConfig{{Type: "reload-test", Name: "fail"}}}
	if e = replaceRuntime(&vc, &nodes, backup, bad); e == nil || errors.Is(e, errRecoveryIncomplete) {
		t.Fatal("rollback failed", e)
	}
	if !vc.(*reloadCore).started {
		t.Fatal("old runtime not restored")
	}
}
func TestLastGoodExternalInputFrozen(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dns.json")
	os.WriteFile(file, []byte(`{"servers":[]}`), 0600)
	c := &conf.Conf{CoresConfig: []conf.CoreConfig{{Type: "sing", SingConfig: &conf.SingConfig{OriginalPath: file}}}}
	b, e := backupConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	os.WriteFile(file, []byte(`bad`), 0600)
	data, e := os.ReadFile(b.config.CoresConfig[0].SingConfig.OriginalPath)
	if e != nil || string(data) != `{"servers":[]}` {
		t.Fatal("last good input changed")
	}
	if c.CoresConfig[0].SingConfig.OriginalPath != file {
		t.Fatal("active config mutated")
	}
}
