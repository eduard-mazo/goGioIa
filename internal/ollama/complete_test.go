package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hola "},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"mundo"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true}`)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/chat", 0)
	out, err := c.Complete(context.Background(), "m", []Message{{Role: "user", Content: "saluda"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "Hola mundo" {
		t.Errorf("Complete = %q", out)
	}
}

func TestCompleteModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/chat", 0)
	if _, err := c.Complete(context.Background(), "m", []Message{{Role: "user", Content: "x"}}, nil); err == nil {
		t.Error("se esperaba error del modelo")
	}
}
