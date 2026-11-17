package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestManagedPayloadAllSupportedWires(t *testing.T) {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("model", "whisper-fixture")
	_ = writer.Close()
	for _, tc := range []struct{ name, path, contentType, body, model string }{
		{"chat", "/v1/chat/completions", "application/json", `{"model":"chat-fixture","messages":[]}`, "chat-fixture"},
		{"gemini", "/v1beta/models/gemini-fixture:streamGenerateContent", "application/json", `{"contents":[]}`, "gemini-fixture"},
		{"transcription", "/v1/audio/transcriptions", writer.FormDataContentType(), form.String(), "whisper-fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://vendor.invalid"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			raw, err := managedPayload(req, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			_ = json.Unmarshal(raw, &got)
			if got["model"] != tc.model {
				t.Fatalf("model=%v", got["model"])
			}
		})
	}
	req, _ := http.NewRequest(http.MethodPost, "https://vendor.invalid/unknown", nil)
	if _, err := managedPayload(req, nil); err == nil {
		t.Fatal("unknown inference wire bypassed admission")
	}
}

func TestManagedReplyKeepsEarlyUsageAcrossLongStream(t *testing.T) {
	stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":4}}}\n\n" + strings.Repeat("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"text\"}}\n\n", 6000) + "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":21}}\n\n"
	var usage map[string]int
	calls := 0
	reader := &managedReply{ReadCloser: io.NopCloser(strings.NewReader(stream)), status: 200, finish: func(status int, raw json.RawMessage, err error) {
		calls++
		if status != 200 || err != nil {
			t.Fatalf("finish=%d,%v", status, err)
		}
		if err := json.Unmarshal(raw, &usage); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := io.CopyBuffer(io.Discard, reader, make([]byte, 113)); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if calls != 1 || usage["input_tokens"] != 12 || usage["cache_read_input_tokens"] != 4 || usage["output_tokens"] != 21 {
		t.Fatalf("settlement=%d usage=%v", calls, usage)
	}
}
