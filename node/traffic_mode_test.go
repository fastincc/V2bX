package node

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestAutomaticTrafficModePersistence(t *testing.T) {
	var enabled atomic.Bool
	var status atomic.Int32
	enabled.Store(true)
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		fmt.Fprintf(w, `{"data":{"version":1,"enabled":%t,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`, enabled.Load())
	}))
	defer srv.Close()
	root := t.TempDir()
	makeController := func(mode string, id int) *Controller {
		api, err := panel.New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: id})
		if err != nil {
			t.Fatal(err)
		}
		return &Controller{apiClient: api, server: &batchCore{counter: counter.NewTrafficCounter()}, Options: &conf.Options{TrafficReportingMode: mode, TrafficSpoolDirectory: root}}
	}
	c := makeController("auto", 1)
	if err := c.configureTrafficMode(); err != nil || !c.reliableTrafficEnabled {
		t.Fatalf("auto negotiation: %v", err)
	}
	if err := c.openTrafficSpool(); err != nil {
		t.Fatal(err)
	}
	if c.trafficSpool.Pending() != nil {
		t.Fatal("expected empty spool")
	}
	c.trafficSpool.Close()
	enabled.Store(false)
	for _, mode := range []string{"auto", "on", "off"} {
		if err := makeController(mode, 1).configureTrafficMode(); err == nil {
			t.Fatalf("%s silently downgraded", mode)
		}
	}
	c = makeController("auto", 2)
	if err := c.configureTrafficMode(); err != nil || c.reliableTrafficEnabled {
		t.Fatalf("fresh disabled node: %v", err)
	}
	status.Store(404)
	if err := makeController("auto", 1).configureTrafficMode(); err == nil {
		t.Fatal("pinned node accepted missing endpoint")
	}
	if err := makeController("auto", 2).configureTrafficMode(); err != nil {
		t.Fatal(err)
	}
	status.Store(500)
	if err := makeController("auto", 2).configureTrafficMode(); err == nil {
		t.Fatal("uncertain error downgraded")
	}
	if err := makeController("off", 2).configureTrafficMode(); err != nil {
		t.Fatal("off should not probe", err)
	}
	status.Store(200)
	enabled.Store(true)
	c = makeController("auto", 1)
	if err := c.configureTrafficMode(); err != nil {
		t.Fatal(err)
	}
	if err := c.openTrafficSpool(); err != nil {
		t.Fatal(err)
	}
	c.trafficSpool.Close()
	if err := os.WriteFile(filepath.Join(root, c.trafficBinding(), "pending.json"), []byte(`broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.openTrafficSpool(); err == nil {
		t.Fatal("corrupt marker accepted")
	}
	c = makeController("auto", 3)
	c.TrafficSpoolDirectory = filepath.Join(root, "not-directory")
	if err := os.WriteFile(c.TrafficSpoolDirectory, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.configureTrafficMode(); err == nil {
		if err = c.openTrafficSpool(); err == nil {
			t.Fatal("invalid storage accepted")
		}
	}
}

func TestTrafficStartupStatusLog(t *testing.T) {
	logger := log.StandardLogger()
	oldHooks := logger.Hooks
	oldOutput := logger.Out
	oldLevel := logger.Level
	defer func() { logger.ReplaceHooks(oldHooks); logger.SetOutput(oldOutput); logger.SetLevel(oldLevel) }()
	logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewGlobal()
	for _, tc := range []struct {
		mode    string
		enabled bool
	}{{"auto", true}, {"auto", false}, {"off", false}, {"on", true}} {
		c := &Controller{reliableTrafficEnabled: tc.enabled, tag: "test-node", Options: &conf.Options{TrafficReportingMode: tc.mode, TrafficBatchMode: tc.enabled}}
		c.logTrafficMode()
		e := hook.LastEntry()
		if e == nil || e.Data["mode"] != tc.mode || e.Data["reliable_reporting"] != tc.enabled {
			t.Fatal("missing startup reporting state")
		}
		if !tc.enabled && e.Data["reason"] == nil {
			t.Fatal("missing legacy reason")
		}
	}
}
