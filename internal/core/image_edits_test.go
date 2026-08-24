package core

import (
	"strings"
	"testing"
)

func validImageEditRequest() *ImageEditRequest {
	return &ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "add a hat",
		Images: []ImageFile{{Filename: "cat.png", ContentType: "image/png", Data: []byte("png")}},
	}
}

func TestValidateImageEditRequest(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ImageEditRequest)
		nilReq  bool
		wantErr string
	}{
		{name: "valid", mutate: func(*ImageEditRequest) {}},
		{name: "valid with mask and fields", mutate: func(r *ImageEditRequest) {
			r.Mask = &ImageFile{Filename: "mask.png", Data: []byte("m")}
			r.Fields = []FormField{{Name: "n", Value: "2"}, {Name: "stream", Value: "false"}, {Name: "size", Value: "1024x1024"}}
		}},
		{name: "nil", nilReq: true, wantErr: "image edit request is required"},
		{name: "missing model", mutate: func(r *ImageEditRequest) { r.Model = " " }, wantErr: "model is required"},
		{name: "missing prompt", mutate: func(r *ImageEditRequest) { r.Prompt = "" }, wantErr: "prompt is required"},
		{name: "missing image", mutate: func(r *ImageEditRequest) { r.Images = nil }, wantErr: "image is required"},
		{name: "empty image", mutate: func(r *ImageEditRequest) { r.Images[0].Data = nil }, wantErr: "image file is empty"},
		{name: "empty mask", mutate: func(r *ImageEditRequest) { r.Mask = &ImageFile{} }, wantErr: "mask file is empty"},
		{name: "n zero", mutate: func(r *ImageEditRequest) { r.Fields = []FormField{{Name: "n", Value: "0"}} }, wantErr: "n must be at least 1"},
		{name: "n not a number", mutate: func(r *ImageEditRequest) { r.Fields = []FormField{{Name: "n", Value: "two"}} }, wantErr: "n must be at least 1"},
		{name: "stream true", mutate: func(r *ImageEditRequest) { r.Fields = []FormField{{Name: "stream", Value: "true"}} }, wantErr: "streaming image edits are not supported"},
		{name: "stream garbage", mutate: func(r *ImageEditRequest) { r.Fields = []FormField{{Name: "stream", Value: "yes"}} }, wantErr: "streaming image edits are not supported"},
		{name: "duplicate stream sneaks true", mutate: func(r *ImageEditRequest) {
			r.Fields = []FormField{{Name: "stream", Value: "false"}, {Name: "stream", Value: "true"}}
		}, wantErr: "streaming image edits are not supported"},
		{name: "duplicate n sneaks zero", mutate: func(r *ImageEditRequest) {
			r.Fields = []FormField{{Name: "n", Value: "2"}, {Name: "n", Value: "0"}}
		}, wantErr: "n must be at least 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *ImageEditRequest
			if !tt.nilReq {
				req = validImageEditRequest()
				tt.mutate(req)
			}
			err := ValidateImageEditRequest(req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if gw, ok := err.(*GatewayError); !ok || gw.StatusCode != 400 {
				t.Fatalf("error should be a 400 gateway error, got %T %v", err, err)
			}
		})
	}
}

func TestImageEditRequestField(t *testing.T) {
	req := &ImageEditRequest{Fields: []FormField{{Name: "size", Value: "256x256"}, {Name: "size", Value: "512x512"}}}
	if v, ok := req.Field("size"); !ok || v != "256x256" {
		t.Errorf("Field(size) = %q, %v; want first value", v, ok)
	}
	if _, ok := req.Field("quality"); ok {
		t.Error("Field(quality) should report absent")
	}
	var nilReq *ImageEditRequest
	if _, ok := nilReq.Field("size"); ok {
		t.Error("nil request should report absent")
	}
}
