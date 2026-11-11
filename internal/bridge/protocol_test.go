package bridge

import (
	"testing"

	"kai/internal/harness"
)

// Qwen Web speaks the strict JSON protocol; DeepSeek Web keeps DSML.
func TestSiteToolProtocols(t *testing.T) {
	s := NewServer(nil)
	cases := []struct {
		site string
		want harness.ToolProtocol
	}{
		{SiteDeepSeek, harness.ProtocolDSML},
		{SiteQwen, harness.ProtocolJSON},
	}
	for _, c := range cases {
		if got := harness.ProtocolOf(s.ForSite(c.site)); got != c.want {
			t.Errorf("ProtocolOf(site=%s) = %v, want %v", c.site, got, c.want)
		}
	}
	if got := harness.ProtocolOf(s); got != harness.ProtocolDSML {
		t.Errorf("ProtocolOf(server) = %v, want DSML", got)
	}
}
