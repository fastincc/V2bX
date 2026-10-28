package panel

import (
	"bytes"
	"errors"
	"github.com/InazumaV/V2bX/conf"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"strings"
	"testing"
)

type privacyTransport func(*http.Request) (*http.Response, error)

func (f privacyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPanelErrorsDoNotExposeCredentials(t *testing.T) {
	const secret = "synthetic-private-api-marker"
	var logs bytes.Buffer
	old := logrus.StandardLogger().Out
	logrus.SetOutput(&logs)
	defer logrus.SetOutput(old)
	for _, mode := range []string{"transport", "http", "debug"} {
		c, err := New(&conf.ApiConfig{APIHost: "https://panel.invalid", Key: secret, NodeType: "anytls", NodeID: 1})
		if err != nil {
			t.Fatal(err)
		}
		c.client.SetRetryCount(1)
		c.Debug()
		calls := 0
		c.client.SetTransport(privacyTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Query().Get("token") != secret {
				t.Error("wire authentication changed")
			}
			if mode == "transport" {
				return nil, errors.New(r.URL.String() + secret)
			}
			status := 503
			if mode == "debug" {
				status = 200
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(secret)), Request: r}, nil
		}))
		err = c.ReportUserTraffic([]UserTraffic{{UID: 1, Upload: 123}})
		if mode != "debug" && err == nil {
			t.Fatal("failure missing")
		}
		if err != nil {
			logs.WriteString(err.Error())
		}
		if calls == 0 {
			t.Fatal("no request")
		}
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "token=") {
		t.Fatal("secret exposed")
	}
}
