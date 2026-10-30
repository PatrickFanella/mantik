package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestLibraryCLIServiceParity(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(storage.NewMemory(), "library-cli")))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "library-cli")
	var out bytes.Buffer
	if err := Run(ctx, []string{"library", "--api-url", server.URL, "--format", "json"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var actual experiment.Catalog
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	want, _ := experiment.BuiltinCatalog()
	if !reflect.DeepEqual(actual, want) {
		t.Fatal("CLI changed service catalogue")
	}
	out.Reset()
	if err := Run(ctx, []string{"library", "--api-url", server.URL, "--id", want.Methods[0].ID}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var pin experiment.MethodPin
	if err := json.Unmarshal(out.Bytes(), &pin); err != nil || !reflect.DeepEqual(pin, want.Methods[0]) {
		t.Fatal("detail changed method pin")
	}
	out.Reset()
	if err := Run(ctx, []string{"library", "--api-url", server.URL}, &out, &out); err != nil || !strings.Contains(out.String(), "unqualified") || !strings.Contains(out.String(), "not an active portfolio") {
		t.Fatal("table hides scope")
	}
	if err := Run(ctx, []string{"library", "--api-url", server.URL, "--id", "missing"}, &out, &out); err == nil {
		t.Fatal("unknown method accepted")
	}
	for _, args := range [][]string{{"--format", "xml"}, {"--api-url", "http://user:password@localhost"}, {"unexpected"}} {
		if err := Run(ctx, append([]string{"library"}, args...), &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestLibraryCLIDoesNotSubstituteLocalMethods(t *testing.T) {
	s := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/library" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "unavailable", 503)
	})))
	defer s.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"library", "--api-url", s.URL}, &out, &out); err == nil || strings.Contains(out.String(), "demo-threshold-v1") {
		t.Fatal("unavailable service replaced by local catalogue")
	}
}
