package ui

import (
	"bytes"
	"strings"
	"testing"

	"comstac/internal/store"
)

func TestPushSectionShowsDeliveriesEscaped(t *testing.T) {
	var out bytes.Buffer
	execute(&out, "push_section", accountsData{
		VAPIDPublicKey: "key",
		PushCount:      1,
		PushTestResult: "test accepted by the push service for 1 of 1 device(s)",
		PushDeliveries: []store.PushDelivery{
			{CreatedAt: "2026-10-02 03:00:00", Kind: "mail", EndpointHost: "fcm.googleapis.com", Status: 201},
			{CreatedAt: "2026-10-02 03:01:00", Kind: "test", EndpointHost: "fcm.googleapis.com", Error: `<b>timeout</b>`},
		},
	})
	html := out.String()
	for _, want := range []string{"1 device subscribed", ">send test</button>", "test accepted by the push service", "fcm.googleapis.com", ">201", "failed · &lt;b&gt;timeout&lt;/b&gt;"} {
		if !strings.Contains(html, want) {
			t.Fatalf("push section missing %q:\n%s", want, html)
		}
	}
}
