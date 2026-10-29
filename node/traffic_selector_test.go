package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	core "github.com/InazumaV/V2bX/core"
)

type trafficSelectorCore struct{ core.Core }

func (*trafficSelectorCore) Type() string                                         { return "sing" }
func (*trafficSelectorCore) Protocols() []string                                  { return []string{"anytls"} }
func (*trafficSelectorCore) AddNode(string, *panel.NodeInfo, *conf.Options) error { return nil }
func (*trafficSelectorCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) {
	return []panel.UserTraffic{{UID: 1, Upload: 20, Download: 30}}, nil
}
func (*trafficSelectorCore) PersistTrafficSnapshot(_ string, save func([]panel.UserTraffic) error) error {
	return save(nil)
}

func TestTrafficModeSurvivesCoreSelection(t *testing.T) {
	core.RegisterCore("traffic-selector-test", func(*conf.CoreConfig) (core.Core, error) { return &trafficSelectorCore{}, nil })
	for _, selection := range []string{"implicit", "named", "explicit", "single"} {
		for _, mode := range []string{"default", "auto", "on", "off", "true", "false"} {
			t.Run(selection+"/"+mode, func(t *testing.T) {
				enabled := mode != "off" && mode != "false"
				var mu sync.Mutex
				requests, legacy, charged := 0, 0, 0
				seen := map[string]bool{}
				dropAck := true
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/server/UniProxy/traffic_capabilities" {
						fmt.Fprint(w, `{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`)
						return
					}
					mu.Lock()
					defer mu.Unlock()
					requests++
					id := r.Header.Get("X-Traffic-Batch-ID")
					if id == "" {
						legacy++
						fmt.Fprint(w, `{"data":true}`)
						return
					}
					if !seen[id] {
						seen[id] = true
						charged++
					}
					if dropAck {
						w.WriteHeader(503)
						return
					}
					fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, id)
				}))
				defer srv.Close()
				raw := map[string]any{"NodeType": "anytls"}
				if selection == "named" {
					raw["CoreName"] = "chosen"
				}
				if selection == "explicit" {
					raw["Core"] = "sing"
				}
				if mode != "default" {
					switch mode {
					case "true":
						raw["TrafficBatchMode"] = true
					case "false":
						raw["TrafficBatchMode"] = false
					default:
						raw["TrafficBatchMode"] = mode
					}
				}
				data, _ := json.Marshal(raw)
				root := t.TempDir()
				makeController := func() *Controller {
					var cfg conf.NodeConfig
					if err := json.Unmarshal(data, &cfg); err != nil {
						t.Fatal(err)
					}
					cfg.Options.TrafficSpoolDirectory = root
					var server core.Core = &trafficSelectorCore{}
					if selection != "single" {
						var err error
						server, err = core.NewSelector([]conf.CoreConfig{{Type: "traffic-selector-test", Name: "chosen"}, {Type: "traffic-selector-test", Name: "other"}})
						if err != nil {
							t.Fatal(err)
						}
					}
					client, err := panel.New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "anytls", NodeID: 1, Key: "synthetic"})
					if err != nil {
						t.Fatal(err)
					}
					c := &Controller{server: server, apiClient: client, Options: &cfg.Options, tag: "test"}
					if err = c.configureTrafficMode(); err != nil {
						t.Fatal(err)
					}
					if err = c.openTrafficSpool(); err != nil {
						t.Fatal(err)
					}
					if err = server.AddNode("test", &panel.NodeInfo{Type: "anytls"}, c.Options); err != nil {
						t.Fatal(err)
					}
					if c.reliableTrafficEnabled != enabled {
						t.Fatal("core selection changed negotiated mode")
					}
					return c
				}
				c := makeController()
				if !enabled {
					if _, err := c.collectAndReportTraffic(); err != nil {
						t.Fatal(err)
					}
					mu.Lock()
					defer mu.Unlock()
					if legacy != 1 {
						t.Fatal("off did not remain legacy")
					}
					return
				}
				if err := c.trafficSpool.Save([]panel.UserTraffic{{UID: 1, Upload: 100, Download: 200}}); err != nil {
					t.Fatal(err)
				}
				if err := c.trafficSpool.Seal(); err != nil {
					t.Fatal(err)
				}
				id := c.trafficSpool.Pending().ID
				if _, err := c.collectAndReportTraffic(); err == nil {
					t.Fatal("lost acknowledgement accepted")
				}
				if c.trafficSpool.Pending() == nil || c.trafficSpool.Pending().ID != id {
					t.Fatal("pending batch lost")
				}
				if err := c.trafficSpool.Close(); err != nil {
					t.Fatal(err)
				}
				c = makeController()
				defer c.trafficSpool.Close()
				if c.trafficSpool.Pending() == nil || c.trafficSpool.Pending().ID != id {
					t.Fatal("restart changed pending batch")
				}
				mu.Lock()
				dropAck = false
				mu.Unlock()
				if _, err := c.collectAndReportTraffic(); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if c.trafficSpool.Pending() != nil || legacy != 0 || charged != 1 || requests < 2 {
					t.Fatalf("invalid replay: legacy=%d charged=%d requests=%d", legacy, charged, requests)
				}
			})
		}
	}
}
