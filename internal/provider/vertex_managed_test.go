package provider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestVertexImportedJSONMintsInMemoryAndRotates(t *testing.T) {
	vertexHome(t)
	google := newVertexGoogle(t)
	// Explicit imported JSON must win without consulting either host ADC path.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-adc.json"))
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(t.TempDir(), "missing-gcloud"))
	p := vertexAt("global", "")
	p.ID = "vault-vertex"
	credentials, _ := json.Marshal(vertexUser("owner-refresh-one"))
	p.Vertex.CredentialsJSON = string(credentials)
	for i := 0; i < 2; i++ {
		auth, err := signVertex(context.Background(), p, "gemini-3.5-flash")
		if err != nil || auth != "Bearer ya29.minted-1" {
			t.Fatalf("inline sign=%q,%v", auth, err)
		}
	}
	if len(google.tokenCalls()) != 1 {
		t.Fatal("inline token cache was not reused")
	}
	credentials, _ = json.Marshal(vertexUser("owner-refresh-two"))
	p.Vertex.CredentialsJSON = string(credentials)
	auth, err := signVertex(context.Background(), p, "gemini-3.5-flash")
	if err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("rotated sign=%q,%v", auth, err)
	}
	if len(google.tokenCalls()) != 2 {
		t.Fatal("changed imported JSON kept old access token")
	}
}
