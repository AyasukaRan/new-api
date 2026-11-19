package service

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type TraceRenderedImage struct {
	Data      string `json:"data"`
	MediaType string `json:"media_type,omitempty"`
}

type traceImagePart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
}

// Chat uses an object with a URL, whereas Responses image parts use a string.
func (r *TraceRenderedResponse) addImagePart(part traceImagePart) {
	switch part.Type {
	case "image_url", "input_image", "output_image":
	default:
		return
	}
	var url string
	switch common.GetJsonType(part.ImageURL) {
	case "string":
		_ = common.Unmarshal(part.ImageURL, &url)
	case "object":
		var image struct {
			URL string `json:"url"`
		}
		if common.Unmarshal(part.ImageURL, &image) == nil {
			url = image.URL
		}
	}
	if len(url) >= 5 && strings.EqualFold(url[:5], "data:") {
		r.addImage("", url)
	}
}

// Other Responses output items can use a non-string result.
func (r *TraceRenderedResponse) addGeneratedImage(format string, result json.RawMessage) {
	var data string
	if common.GetJsonType(result) == "string" && common.Unmarshal(result, &data) == nil && data != "" {
		r.addImage(format, data)
	}
}

type traceImageSource struct {
	Type          string `json:"type"`
	Data          string `json:"data"`
	MediaType     string `json:"media_type"`
	MimeType      string `json:"mimeType"`
	SnakeMimeType string `json:"mime_type"`
}

// Only serialize embedded raster bytes. Never resolve a URL. A missing or
// invalid image gets one unavailable marker; SSE can repeat the same image.
func (r *TraceRenderedResponse) addImage(mediaType, data string) {
	image := traceRasterImage(mediaType, data)
	if _, seen := r.seenImages[image.Data]; seen {
		return
	}
	if r.seenImages == nil {
		r.seenImages = make(map[string]struct{})
	}
	r.seenImages[image.Data] = struct{}{}
	r.Images = append(r.Images, image)
}

// Generation APIs declare "png" while chat messages declare "image/png".
func traceImageMIME(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return ""
	}
	format = strings.TrimPrefix(format, "image/")
	if format == "jpg" {
		format = "jpeg"
	}
	return "image/" + format
}

func traceRasterImage(format, data string) TraceRenderedImage {
	mime := traceImageMIME(format)
	data = strings.TrimSpace(data)
	if head, body, uri := strings.Cut(data, ","); uri {
		declared, encoding, ok := strings.Cut(head, ";")
		if !ok || len(declared) < 5 || !strings.EqualFold(declared[:5], "data:") || !strings.EqualFold(encoding, "base64") {
			return TraceRenderedImage{}
		}
		headerMIME := strings.ToLower(declared[5:])
		if !strings.HasPrefix(headerMIME, "image/") || (mime != "" && mime != traceImageMIME(headerMIME)) {
			return TraceRenderedImage{}
		}
		mime, data = traceImageMIME(headerMIME), body
	}
	if strings.ContainsAny(data, " \t\r\n") {
		data = strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n':
				return -1
			}
			return r
		}, data)
	}
	encoding := base64.RawStdEncoding
	if strings.HasSuffix(data, "=") {
		encoding = base64.StdEncoding
	}
	reader := base64.NewDecoder(encoding, strings.NewReader(data))
	var header [16]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return TraceRenderedImage{}
	}
	detected := http.DetectContentType(header[:])
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return TraceRenderedImage{}
	}
	if mime != "" && mime != detected {
		return TraceRenderedImage{}
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return TraceRenderedImage{}
	}
	return TraceRenderedImage{Data: data, MediaType: detected}
}

// The Images API emits complete snapshots for partial previews before the final
// generation or edit. Only the final event belongs in the saved conversation.
func renderOpenAIImageResponse(events []string) *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{}
	for _, event := range events {
		var payload struct {
			Type         string `json:"type"`
			OutputFormat string `json:"output_format"`
			Base64       string `json:"b64_json"`
			Data         []struct {
				Base64 string `json:"b64_json"`
			} `json:"data"`
		}
		if common.UnmarshalJsonStr(event, &payload) != nil {
			continue
		}
		switch payload.Type {
		case "image_generation.completed", "image_edit.completed":
			if payload.Base64 != "" {
				rendered.addImage(payload.OutputFormat, payload.Base64)
			}
		case "":
			for _, image := range payload.Data {
				if image.Base64 != "" {
					rendered.addImage(payload.OutputFormat, image.Base64)
				}
			}
		}
	}
	return rendered
}
