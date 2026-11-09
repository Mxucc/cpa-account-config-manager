package manager

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway's channel list grows with the operator's own configuration, and the
// management answer that carries it is read whole. A cap that fits a small host
// truncates a large one, and the truncation surfaces as "the channel list could
// not be loaded" on a gateway that is perfectly healthy — which is exactly how a
// finished community sign-in failed to become a channel.
func TestManagementClientReadsAChannelListLargerThanTheOldCap(t *testing.T) {
	// 100 KiB of JSON: past the old 64 KiB limit, well inside the new one.
	entry := `{"name":"` + strings.Repeat("n", 500) + `","base-url":"https://example.test/v1","api-key-entries":[{"api-key":"k"}]}`
	body := `{"openai-compatibility":[` + strings.Join(repeatEntry(entry, 200), ",") + `]}`
	if len(body) <= 64<<10 {
		t.Fatalf("fixture is %d bytes, which does not exercise the old cap", len(body))
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()

	client, errClient := newManagementClient(server.URL, "management-secret", server.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	entries, supported, errRead := client.getAIProviderChannel(context.Background(), "openai-compatibility")
	if errRead != nil {
		t.Fatalf("getAIProviderChannel() error = %v", errRead)
	}
	if !supported {
		t.Fatal("a served channel list was reported as unsupported")
	}
	if len(entries) != 200 {
		t.Fatalf("entries = %d, want 200", len(entries))
	}
}

func repeatEntry(entry string, count int) []string {
	out := make([]string, count)
	for index := range out {
		out[index] = entry
	}
	return out
}
