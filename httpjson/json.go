package httpjson

import (
	"encoding/json"
	"net/http"
)

type Response struct {
	Status int
	Body   any
}

// M is a helper type to create a map[string]interface{}
type M map[string]interface{}

type Handler func(w http.ResponseWriter, r *http.Request) *Response

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := (h)(w, r)
	Write(w, resp.Status, resp.Body)
}

func Write(w http.ResponseWriter, statusCode int, v interface{}) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = enc.Encode(v)
}
