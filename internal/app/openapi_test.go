package app

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// TestRoutesMatchOpenAPI keeps api/openapi.yaml and the registered routes in
// sync: every route must be documented and every documented operation served.
func TestRoutesMatchOpenAPI(t *testing.T) {
	a, _ := newApp(t, nil)
	served := map[string]bool{}
	err := chi.Walk(a.Server.API(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" || route == "/*" {
			return nil
		}
		served[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(served) < 5 {
		t.Fatalf("walked only %d routes", len(served))
	}
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for p, ops := range doc.Paths {
		for m := range ops {
			switch m {
			case "get", "post", "put", "patch", "delete":
				documented[strings.ToUpper(m)+" "+p] = true
			}
		}
	}
	var missingDoc, missingRoute []string
	for k := range served {
		if !documented[k] {
			missingDoc = append(missingDoc, k)
		}
	}
	for k := range documented {
		if !served[k] {
			missingRoute = append(missingRoute, k)
		}
	}
	sort.Strings(missingDoc)
	sort.Strings(missingRoute)
	if len(missingDoc) > 0 {
		t.Errorf("routes missing from api/openapi.yaml: %v", missingDoc)
	}
	if len(missingRoute) > 0 {
		t.Errorf("operations in api/openapi.yaml that are not served: %v", missingRoute)
	}
}
