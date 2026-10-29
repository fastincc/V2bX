package conf

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"encoding/json"

	"github.com/InazumaV/V2bX/common/json5"
)

type NodeConfig struct {
	ApiConfig ApiConfig `json:"-"`
	Options   Options   `json:"-"`
}

type rawNodeConfig struct {
	Include string          `json:"Include"`
	ApiRaw  json.RawMessage `json:"ApiConfig"`
	OptRaw  json.RawMessage `json:"Options"`
}

type ApiConfig struct {
	APIHost      string `json:"ApiHost"`
	APISendIP    string `json:"ApiSendIP"`
	NodeID       int    `json:"NodeID"`
	Key          string `json:"ApiKey"`
	NodeType     string `json:"NodeType"`
	Timeout      int    `json:"Timeout"`
	RuleListPath string `json:"RuleListPath"`
}

func (n *NodeConfig) UnmarshalJSON(data []byte) (err error) {
	rn := rawNodeConfig{}
	err = json.Unmarshal(data, &rn)
	if err != nil {
		return err
	}
	if len(rn.Include) != 0 {
		file, _ := strings.CutPrefix(rn.Include, ":")
		switch file {
		case "http", "https":
			rsp, err := http.Get(file)
			if err != nil {
				return err
			}
			defer rsp.Body.Close()
			data, err = io.ReadAll(json5.NewTrimNodeReader(rsp.Body))
			if err != nil {
				return fmt.Errorf("open include file error: %s", err)
			}
		default:
			f, err := os.Open(rn.Include)
			if err != nil {
				return fmt.Errorf("open include file error: %s", err)
			}
			defer f.Close()
			data, err = io.ReadAll(json5.NewTrimNodeReader(f))
			if err != nil {
				return fmt.Errorf("open include file error: %s", err)
			}
		}
		err = json.Unmarshal(data, &rn)
		if err != nil {
			return fmt.Errorf("unmarshal include file error: %s", err)
		}
	}

	n.ApiConfig = ApiConfig{
		APIHost: "http://127.0.0.1",
		Timeout: 30,
	}
	if len(rn.ApiRaw) > 0 {
		err = json.Unmarshal(rn.ApiRaw, &n.ApiConfig)
		if err != nil {
			return
		}
	} else {
		err = json.Unmarshal(data, &n.ApiConfig)
		if err != nil {
			return
		}
	}

	n.Options = Options{
		ListenIP:   "0.0.0.0",
		SendIP:     "0.0.0.0",
		CertConfig: NewCertConfig(),
	}
	if len(rn.OptRaw) > 0 {
		err = json.Unmarshal(rn.OptRaw, &n.Options)
		if err != nil {
			return
		}
	} else {
		err = json.Unmarshal(data, &n.Options)
		if err != nil {
			return
		}
	}
	return
}

type Options struct {
	TrafficCheckpointSeconds int             `json:"TrafficCheckpointSeconds"`
	TrafficSpoolMaxBytes     int64           `json:"TrafficSpoolMaxBytes"`
	TrafficSpoolMaxBatches   int             `json:"TrafficSpoolMaxBatches"`
	TrafficReportingMode     string          `json:"-"`
	TrafficBatchMode         bool            `json:"TrafficBatchMode"`
	TrafficSpoolDirectory    string          `json:"TrafficSpoolDirectory"`
	Name                     string          `json:"Name"`
	Core                     string          `json:"Core"`
	CoreName                 string          `json:"CoreName"`
	ListenIP                 string          `json:"ListenIP"`
	SendIP                   string          `json:"SendIP"`
	DeviceOnlineMinTraffic   int64           `json:"DeviceOnlineMinTraffic"`
	ReportMinTraffic         int64           `json:"ReportMinTraffic"`
	LimitConfig              LimitConfig     `json:"LimitConfig"`
	RawOptions               json.RawMessage `json:"RawOptions"`
	XrayOptions              *XrayOptions    `json:"XrayOptions"`
	SingOptions              *SingOptions    `json:"SingOptions"`
	Hysteria2ConfigPath      string          `json:"Hysteria2ConfigPath"`
	CertConfig               *CertConfig     `json:"CertConfig"`
}

func (o *Options) UnmarshalJSON(data []byte) error {
	type opt Options
	raw := struct {
		*opt
		Mode json.RawMessage `json:"TrafficBatchMode"`
	}{opt: (*opt)(o)}
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}
	o.TrafficReportingMode = "auto"
	o.TrafficBatchMode = false
	if len(raw.Mode) != 0 {
		switch strings.TrimSpace(string(raw.Mode)) {
		case "null":
			return fmt.Errorf("TrafficBatchMode must not be null")
		case "true":
			o.TrafficReportingMode = "on"
		case "false":
			o.TrafficReportingMode = "off"
		default:
			if json.Unmarshal(raw.Mode, &o.TrafficReportingMode) != nil {
				return fmt.Errorf("TrafficBatchMode must be auto, on, off, or a boolean")
			}
		}
		switch o.TrafficReportingMode {
		case "auto", "off":
		case "on":
			o.TrafficBatchMode = true
		default:
			return fmt.Errorf("TrafficBatchMode must be auto, on, off, or a boolean")
		}
	}
	switch o.Core {
	case "xray":
		o.XrayOptions = NewXrayOptions()
		return json.Unmarshal(data, o.XrayOptions)
	case "sing":
		o.SingOptions = NewSingOptions()
		return json.Unmarshal(data, o.SingOptions)
	case "hysteria2":
		o.RawOptions = data
		return nil
	default:
		o.Core = ""
		o.RawOptions = data
	}
	return nil
}

// MarshalJSON preserves the requested policy, not the negotiated runtime flag.
func (o Options) MarshalJSON() ([]byte, error) {
	type opt Options
	mode := o.TrafficReportingMode
	if mode == "" {
		mode = "auto"
		if o.TrafficBatchMode {
			mode = "on"
		}
	}
	return json.Marshal(struct {
		*opt
		Mode string `json:"TrafficBatchMode"`
	}{(*opt)(&o), mode})
}
