package node

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportingStatusProtocol(t *testing.T) {
	var b bytes.Buffer
	writeReportingStatus(&b, "https://secret@example.invalid", "anytls", 102, "enabled", "auto")
	line := b.String()
	if !strings.HasPrefix(line, "V2BX_REPORTING_STATUS_V1 binding=") || !strings.Contains(line, "node_id=102 node_type=anytls state=enabled mode=auto\n") {
		t.Fatal("invalid status protocol")
	}
	if strings.Contains(line, "secret") || strings.Contains(line, "example.invalid") {
		t.Fatal("status leaked host")
	}
	fields := strings.Fields(line)
	if len(fields) != 6 || len(strings.TrimPrefix(fields[1], "binding=")) != 64 {
		t.Fatal("invalid binding")
	}
	b.Reset()
	writeReportingStatus(&b, "https://secret@example.invalid", "anytls", 102, "unknown", "auto")
	if strings.Fields(b.String())[1] != fields[1] {
		t.Fatal("state changes identity")
	}
	b.Reset()
	writeReportingStatus(&b, "https://other.invalid", "anytls", 102, "disabled", "off")
	if strings.Fields(b.String())[1] == fields[1] {
		t.Fatal("different panels share identity")
	}
	b.Reset()
	writeReportingStatus(&b, "hidden", "bad\n\x1b[31m", 1, "bad\n", "bad\n")
	if strings.Count(b.String(), "\n") != 1 || strings.Contains(b.String(), "\x1b") || !strings.Contains(b.String(), "node_type=unknown state=unknown mode=unknown") {
		t.Fatal("unsafe output")
	}
}

func TestReportingStatusProtocolAliases(t *testing.T) {
	var a, b bytes.Buffer
	writeReportingStatus(&a, "panel", "V2RAY", 1, "unknown", "auto")
	writeReportingStatus(&b, "panel", "vmess", 1, "unknown", "auto")
	if a.String() != b.String() {
		t.Fatal("initial node registration differs from normalized client identity")
	}
}
