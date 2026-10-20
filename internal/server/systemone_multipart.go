package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
)

// systemOneRequestPart names the multipart part that holds the JSON request.
const systemOneRequestPart = "request"

// isMultipartFormData reports whether a Content-Type is multipart/form-data.
func isMultipartFormData(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "multipart/form-data"
}

// systemOneMultipartToJSON turns TypeSafe's multipart System One request into
// the equivalent JSON request. The multipart form, which TypeSafe recommends
// for images and its SDKs send for them, holds the JSON request as its first
// part, named "request", and each image as a part an asset names with
// "part". Each such asset gets the image inline as data_base64 instead, a
// form TypeSafe accepts too, so guardrails, the cache, and failover handle
// one JSON shape and every target receives a request it can read. The rest
// of the request, the questions' order included, is kept as sent. The rules
// TypeSafe applies to the form are checked here, so a malformed form is
// reported the same way.
func systemOneMultipartToJSON(contentType string, body []byte) ([]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, errors.New("multipart request has no boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])

	first, err := reader.NextPart()
	if err != nil {
		return nil, fmt.Errorf("multipart request has no parts: %w", err)
	}
	if first.FormName() != systemOneRequestPart {
		return nil, fmt.Errorf("the first part must be named %q and hold the JSON request", systemOneRequestPart)
	}
	request, err := io.ReadAll(first)
	if err != nil {
		return nil, err
	}

	images := map[string]multipartImage{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := part.FormName()
		switch {
		case strings.TrimSpace(name) == "":
			return nil, errors.New("every image part needs a name")
		case name == systemOneRequestPart:
			return nil, fmt.Errorf("only the first part may be named %q", systemOneRequestPart)
		}
		if _, dup := images[name]; dup {
			return nil, fmt.Errorf("part names must be unique; %q repeats", name)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		images[name] = multipartImage{contentType: part.Header.Get("Content-Type"), data: data}
	}

	var top struct {
		Assets map[string]map[string]json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(request, &top); err != nil {
		return nil, fmt.Errorf("the request part is not a JSON request: %w", err)
	}
	used := make(map[string]struct{}, len(images))
	for ref, asset := range top.Assets {
		rawPart, ok := asset["part"]
		if !ok {
			continue
		}
		var partName string
		if err := json.Unmarshal(rawPart, &partName); err != nil {
			return nil, fmt.Errorf("assets.%s.part must be a string", ref)
		}
		image, ok := images[partName]
		if !ok {
			return nil, fmt.Errorf("assets.%s names part %q, which the request does not hold", ref, partName)
		}
		if _, dup := used[partName]; dup {
			return nil, fmt.Errorf("part %q is used by more than one asset", partName)
		}
		used[partName] = struct{}{}
		format, err := image.format(asset["format"])
		if err != nil {
			return nil, fmt.Errorf("assets.%s: %w", ref, err)
		}
		delete(asset, "part")
		asset["format"], _ = json.Marshal(format)
		asset["data_base64"], _ = json.Marshal(base64.StdEncoding.EncodeToString(image.data))
	}
	for name := range images {
		if _, ok := used[name]; !ok {
			return nil, fmt.Errorf("part %q is not used by any asset", name)
		}
	}
	if len(used) == 0 {
		return request, nil
	}
	assets, err := json.Marshal(top.Assets)
	if err != nil {
		return nil, err
	}
	return replaceTopLevelMember(request, "assets", assets)
}

type multipartImage struct {
	contentType string
	data        []byte
}

// format returns the asset's image format: the one the asset states, or,
// with none, the part's content type, or the format its bytes show.
func (i multipartImage) format(stated json.RawMessage) (string, error) {
	var format string
	if len(stated) > 0 && json.Unmarshal(stated, &format) == nil && format != "" {
		return format, nil
	}
	mediaType, _, _ := mime.ParseMediaType(i.contentType)
	switch mediaType {
	case "image/png":
		return "png", nil
	case "image/jpeg", "image/jpg":
		return "jpg", nil
	case "", "application/octet-stream":
	default:
		return "", fmt.Errorf("image parts must be PNG or JPEG, not %s", mediaType)
	}
	switch http.DetectContentType(i.data) {
	case "image/png":
		return "png", nil
	case "image/jpeg":
		return "jpg", nil
	}
	return "", errors.New("image part is not a PNG or JPEG image")
}
