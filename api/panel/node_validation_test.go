package panel
import("fmt";"testing";"net/http";"net/http/httptest";"github.com/InazumaV/V2bX/conf")
func TestNodeConfigValidationAndCommit(t *testing.T){
 for _,body:=range []string{`{}`,`null`,`{"server_port":1,"base_config":{"push_interval":null,"pull_interval":60}}`,`{"server_port":1,"base_config":{"push_interval":1,"pull_interval":60},"routes":[{"action":"dns","match":[]}]}`,`{"server_port":1,"base_config":{"push_interval":1,"pull_interval":60},"routes":[{"action":"block","match":["["]}]}`,`{"server_port":1,"base_config":{"push_interval":1,"pull_interval":60},"routes":[{"action":"dns","match":[1]}]}`} {
 s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,body)}));c,_:=New(&conf.ApiConfig{APIHost:s.URL,NodeType:"anytls",Key:"synthetic"});for i:=0;i<2;i++{if n,e:=c.GetNodeInfo();e==nil||n!=nil{t.Fatalf("invalid config accepted: %d",i)}};s.Close()
 }
 for _,protocol:=range []string{"anytls","v2ray","vless","shadowsocks","trojan","tuic","hysteria","hysteria2"}{
 s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("ETag","synthetic");if r.Header.Get("If-None-Match")!="" {w.WriteHeader(304);return};fmt.Fprint(w,`{"server_port":12345,"base_config":{"push_interval":"60","pull_interval":30},"unknown_future_field":true}`)}));c,_:=New(&conf.ApiConfig{APIHost:s.URL,NodeType:protocol});n,e:=c.GetNodeInfo();if e!=nil||n==nil{t.Fatal(protocol,e)};retry,e:=c.GetNodeInfo();if e!=nil||retry==nil{t.Fatal("uncommitted config suppressed")};c.CommitNodeInfo(n);if retry,e=c.GetNodeInfo();retry!=nil||e!=nil{t.Fatal("commit not respected")};s.Close()
 }
}
func TestNodeIntervals(t *testing.T){for _,v:=range []any{nil,true,[]int{},-1,1e30,"invalid","-1"}{if _,e:=intervalToTime(v);e==nil{t.Fatal("bad interval accepted")}};for _,v:=range []any{0,1,"60",float64(60)}{if _,e:=intervalToTime(v);e!=nil{t.Fatal(e)}}}
