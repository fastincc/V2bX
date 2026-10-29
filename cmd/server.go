package cmd

import (
	"errors"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	config string
	watch  bool
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run V2bX server",
	RunE:  serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/V2bX/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

func serverHandle(_ *cobra.Command, _ []string) (result error) {
	showVersion()
	c := conf.New()
	err := c.LoadFromPath(config)
	if err != nil {
		log.WithField("err", err).Error("Load config file failed")
		return errors.New("server startup failed; inspect protected log")
	}
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if c.LogConfig.Output != "" {
		f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.WithField("err", err).Error("Open log file failed, using stdout instead")
		}
		if err == nil {
			log.SetOutput(f)
		}
	}
	limiter.Init()
	log.Info("Start V2bX...")
	vc, err := vCore.NewCore(c.CoresConfig)
	if err != nil {
		log.WithField("err", err).Error("new core failed")
		return errors.New("server startup failed; inspect protected log")
	}
	var lifecycle sync.Mutex
	nodes := node.New()
	defer func() {
		lifecycle.Lock()
		defer lifecycle.Unlock()
		result = errors.Join(result, nodes.Close())
		if vc != nil {
			result = errors.Join(result, vc.Close())
		}
	}()
	err = vc.Start()
	if err != nil {
		log.WithField("err", err).Error("Start core failed")
		return errors.New("server startup failed; inspect protected log")
	}
	log.Info("Core ", vc.Type(), " started")
	err = nodes.Start(c.NodeConfig, vc)
	if err != nil {
		log.WithField("err", err).Error("Run nodes failed")
		return errors.New("server startup failed; inspect protected log")
	}
	log.Info("Nodes started")
	xdns := os.Getenv("XRAY_DNS_PATH")
	sdns := os.Getenv("SING_DNS_PATH")
	if watch {
		err = c.Watch(config, xdns, sdns, func(candidate *conf.Conf) error {
			lifecycle.Lock()
			defer lifecycle.Unlock()
			if closeErr := nodes.Close(); closeErr != nil {
				log.WithError(closeErr).Error("Traffic drain failed; reload cancelled")
				return errors.New("configuration apply failed")
			}
			err = vc.Close()
			if err != nil {
				log.WithField("err", err).Error("Restart node failed")
				return errors.New("configuration apply failed")
			}
			vc, err = vCore.NewCore(candidate.CoresConfig)
			if err != nil {
				log.WithField("err", err).Error("New core failed")
				return errors.New("configuration apply failed")
			}
			err = vc.Start()
			if err != nil {
				log.WithField("err", err).Error("Start core failed")
				return errors.New("configuration apply failed")
			}
			log.Info("Core ", vc.Type(), " restarted")
			err = nodes.Start(candidate.NodeConfig, vc)
			if err != nil {
				log.WithField("err", err).Error("Run nodes failed")
				return errors.New("configuration apply failed")
			}
			log.Info("Nodes restarted")
			runtime.GC()
			return nil
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			return errors.New("configuration watch failed")
		}
	}
	// clear memory
	runtime.GC()
	// wait exit signal
	{
		osSignals := make(chan os.Signal, 1)
		signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(osSignals)
		<-osSignals
	}
	return nil
}
