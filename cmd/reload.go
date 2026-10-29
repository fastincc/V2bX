package cmd

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/node"
	"os"
	"path/filepath"
)

// The backup contains private copies of direct external inputs. It never writes
// old data over an operator's files when rolling back.
var errRecoveryIncomplete = errors.New("runtime recovery incomplete; further reloads blocked")

type configBackup struct {
	config *conf.Conf
	dir    string
}

func backupConfig(c *conf.Conf) (*configBackup, error) {
	dir, e := os.MkdirTemp("", "v2bx-last-good-")
	if e != nil {
		return nil, e
	}
	b := &configBackup{config: c.Clone(), dir: dir}
	ok := false
	defer func() {
		if !ok {
			b.Close()
		}
	}()
	i := 0
	copyFile := func(p *string) error {
		if *p == "" {
			return nil
		}
		data, e := os.ReadFile(*p)
		if e != nil {
			return fmt.Errorf("configuration input unavailable")
		}
		i++
		dest := filepath.Join(dir, fmt.Sprintf("%d%s", i, filepath.Ext(*p)))
		if e = os.WriteFile(dest, data, 0600); e != nil {
			return e
		}
		*p = dest
		return nil
	}
	for j := range b.config.CoresConfig {
		k := &b.config.CoresConfig[j]
		if k.SingConfig != nil {
			if e = copyFile(&k.SingConfig.OriginalPath); e != nil {
				return nil, e
			}
		}
		if x := k.XrayConfig; x != nil {
			for _, p := range []*string{&x.DnsConfigPath, &x.RouteConfigPath, &x.InboundConfigPath, &x.OutboundConfigPath} {
				if e = copyFile(p); e != nil {
					return nil, e
				}
			}
		}
	}
	for j := range b.config.NodeConfig {
		o := &b.config.NodeConfig[j].Options
		if e = copyFile(&o.Hysteria2ConfigPath); e != nil {
			return nil, e
		}
		if cc := o.CertConfig; cc != nil && cc.CertMode == "file" {
			for _, p := range []*string{&cc.CertFile, &cc.KeyFile} {
				if e = copyFile(p); e != nil {
					return nil, e
				}
			}
		}
	}
	ok = true
	return b, nil
}
func (b *configBackup) Close() {
	if b != nil {
		_ = os.RemoveAll(b.dir)
	}
}
func newCoreSafely(c *conf.Conf) (core vCore.Core, err error) {
	defer func() {
		if recover() != nil {
			core = nil
			err = errors.New("core configuration rejected")
		}
	}()
	return vCore.NewCore(c.Clone().CoresConfig)
}

// A failed replacement is closed before rollback. If it cannot be safely
// closed, retain it for recovery instead of destroying unpersisted counters.
func replaceRuntime(vc *vCore.Core, nodes **node.Node, backup *configBackup, candidate *conf.Conf) error {
	next, err := newCoreSafely(candidate)
	if err != nil {
		return err
	}
	if err = (*nodes).CloseForReload(); err != nil {
		_ = next.Close()
		if errors.Is(err, node.ErrRestoreIncomplete) {
			return errors.Join(err, errRecoveryIncomplete)
		}
		return fmt.Errorf("reload cancelled; previous nodes restored where possible: %w", err)
	}
	oldNodes := *nodes
	rollback := func(cause error) error {
		oldCore, e := newCoreSafely(backup.config)
		if e != nil {
			return errors.Join(cause, e, errRecoveryIncomplete)
		}
		*vc = oldCore
		*nodes = oldNodes
		if e = oldCore.Start(); e != nil {
			return errors.Join(cause, e, errRecoveryIncomplete)
		}
		if e = oldNodes.RestoreOn(oldCore, backup.config.NodeConfig); e != nil {
			return errors.Join(cause, e, errRecoveryIncomplete)
		}
		return cause
	}
	if err = (*vc).Close(); err != nil {
		_ = next.Close()
		return rollback(fmt.Errorf("old core close failed: %w", err))
	}
	*vc = next
	*nodes = node.New()
	if err = next.Start(); err == nil {
		err = (*nodes).Start(candidate.NodeConfig, next)
	}
	if err == nil {
		return nil
	}
	cause := fmt.Errorf("candidate startup failed: %w", err)
	if closeErr := (*nodes).CloseForReload(); closeErr != nil {
		return errors.Join(cause, errRecoveryIncomplete, fmt.Errorf("candidate retained with unsaved traffic: %w", closeErr))
	}
	if closeErr := next.Close(); closeErr != nil {
		return errors.Join(cause, closeErr, errRecoveryIncomplete)
	}
	return rollback(cause)
}
