package proxy

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/sjson"
)

func buildImageStreamPayload(eventType, responseFormat string, img imageCallResult, partialImageIndex int64, usageRaw []byte) []byte {
	payload := []byte(`{"type":""}`)
	payload, _ = sjson.SetBytes(payload, "type", eventType)

	if strings.HasSuffix(eventType, ".partial_image") {
		payload, _ = sjson.SetBytes(payload, "partial_image_index", partialImageIndex)
	}

	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		payload, _ = sjson.SetBytes(payload, "url", "data:"+mimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
	} else {
		payload, _ = sjson.SetBytes(payload, "b64_json", img.Result)
	}

	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		payload, _ = sjson.SetRawBytes(payload, "usage", usageRaw)
	}

	return payload
}
