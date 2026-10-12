package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type routingTransport func(*http.Request) (*http.Response, error)

func (transport routingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func mockRoutingKubectl(t *testing.T, ingress, proxies, child string, ingressError, proxyError bool) {
	t.Helper()
	directory := t.TempDir()
	files := map[string]string{
		"ingress.json": ingress,
		"proxies.json": proxies,
		"child.json":   child,
		"kubectl": `#!/bin/sh
case "$2" in
ingress)
  if [ "$ROUTING_INGRESS_ERROR" = "true" ]; then
    echo 'Error from server (Forbidden): cannot list ingresses' >&2
    exit 1
  fi
  cat "$ROUTING_FIXTURES/ingress.json"
  ;;
httpproxies.projectcontour.io)
  if [ "$ROUTING_PROXY_ERROR" = "true" ]; then
    echo "error: the server doesn't have a resource type httpproxies.projectcontour.io" >&2
    exit 1
  fi
  if [ "$3" = "-n" ]; then
    cat "$ROUTING_FIXTURES/proxies.json"
  elif [ "$3" = "child" ] && [ "$5" = "shared" ]; then
    cat "$ROUTING_FIXTURES/child.json"
  else
    echo 'Error from server (NotFound): HTTPProxy not found' >&2
    exit 1
  fi
  ;;
*) exit 1 ;;
esac
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ROUTING_FIXTURES", directory)
	t.Setenv("ROUTING_INGRESS_ERROR", fmt.Sprint(ingressError))
	t.Setenv("ROUTING_PROXY_ERROR", fmt.Sprint(proxyError))
}

const routingIngressFixture = `{"items":[{"metadata":{"name":"ingress"},"spec":{"rules":[{"host":"ingress.example","http":{"paths":[{"path":"/"}]}}]}}]}`

func routingProxyFixture(status, host string) string {
	return fmt.Sprintf(`{"items":[{"metadata":{"name":"root","namespace":"viya"},"spec":{"virtualhost":{"fqdn":%q},"routes":[{"conditions":[{"prefix":"/"}],"services":[{"name":"sas-logon-app","port":80}]}]},"status":{"currentStatus":%q,"description":"controller diagnostic"}}]}`, host, status)
}

func TestHTTPRoutingDiscovery(t *testing.T) {
	for _, test := range []struct {
		name, ingress, proxies, controller, status string
		proxyAbsent, ingressError                  bool
		routes, proxiesTotal                       int
	}{
		{"ingress only without CRD", routingIngressFixture, "", "ingress", "HEALTHY", true, false, 1, 0},
		{"contour only", `{"items":[]}`, routingProxyFixture("Valid", "contour.example"), "contour", "HEALTHY", false, false, 1, 1},
		{"mixed", routingIngressFixture, routingProxyFixture("Valid", "contour.example"), "mixed", "HEALTHY", false, false, 2, 1},
		{"empty FQDN fallback", `{"items":[]}`, routingProxyFixture("Valid", ""), "contour", "HEALTHY", false, false, 1, 1},
		{"no resources", `{"items":[]}`, `{"items":[]}`, "none", "DEGRADED", false, false, 0, 0},
		{"unknown", `{"items":[]}`, routingProxyFixture("Unknown", "contour.example"), "contour", "DEGRADED", false, false, 1, 1},
		{"empty status", `{"items":[]}`, routingProxyFixture("", "contour.example"), "contour", "DEGRADED", false, false, 1, 1},
		{"invalid", `{"items":[]}`, routingProxyFixture("Invalid", "contour.example"), "contour", "HEALTHY", false, false, 1, 1},
		{"orphaned", `{"items":[]}`, routingProxyFixture("Orphaned", "contour.example"), "contour", "HEALTHY", false, false, 1, 1},
		{"invalid without routes", `{"items":[]}`, `{"items":[{"metadata":{"name":"invalid","namespace":"viya"},"status":{"currentStatus":"Invalid"}}]}`, "contour", "HEALTHY", false, false, 0, 1},
		{"partial discovery failure", "", routingProxyFixture("Valid", "contour.example"), "contour", "DEGRADED", false, true, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			mockRoutingKubectl(t, test.ingress, test.proxies, "", test.ingressError, test.proxyAbsent)
			client := &http.Client{Transport: routingTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host == "contour.example" && request.URL.Scheme != "https" {
					t.Error("HTTPProxy FQDN must use HTTPS")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			check := checkViyaHTTPRouting(client, "viya", "", ViyaAPIConfig{Enabled: true, BaseURL: "http://fallback.example/"}, "", false)
			if check.Name != "HTTP Routing Endpoints" || check.Status != test.status {
				t.Fatalf("unexpected check: %+v", check)
			}
			summary := check.Routing
			if summary.ControllerType != test.controller || summary.RoutesTested != test.routes || summary.HTTPProxiesTotal != test.proxiesTotal {
				t.Fatalf("unexpected summary: %+v", summary)
			}
			if test.name == "empty FQDN fallback" && summary.Routes[0].URL != "http://fallback.example/" {
				t.Errorf("unexpected fallback URL: %s", summary.Routes[0].URL)
			}
			if test.proxiesTotal > 0 && test.routes > 0 {
				proxy := summary.HTTPProxies[0]
				if proxy.Status.Description != "controller diagnostic" || len(proxy.Spec.Routes[0].Services) != 1 {
					t.Errorf("resource details lost: %+v", proxy)
				}
			}
			encoded, err := json.Marshal(summary)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]interface{}
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"controller_type", "httpproxies_total", "httpproxies_valid", "httpproxies_invalid", "httpproxies_orphaned", "routes_tested", "auth_restricted_routes", "failed_routes"} {
				if _, exists := fields[field]; !exists {
					t.Errorf("missing JSON field %s", field)
				}
			}
		})
	}
}

func TestHTTPRoutingResponses(t *testing.T) {
	for _, test := range []struct {
		name         string
		code         int
		err          error
		strict       bool
		status       string
		failed, auth int
	}{
		{"success", 200, nil, false, "HEALTHY", 0, 0},
		{"redirect", 302, nil, false, "HEALTHY", 0, 0},
		{"unauthorized", 401, nil, false, "HEALTHY", 0, 1},
		{"forbidden", 403, nil, false, "HEALTHY", 0, 1},
		{"strict opt in", 403, nil, true, "DEGRADED", 0, 1},
		{"not found", 404, nil, false, "HEALTHY", 1, 0},
		{"bad request", 400, nil, false, "HEALTHY", 1, 0},
		{"server error", 500, nil, false, "HEALTHY", 1, 0},
		{"unavailable", 503, nil, false, "HEALTHY", 1, 0},
		{"connection failure", 0, errors.New("connection refused"), false, "HEALTHY", 1, 0},
		{"TLS failure", 0, errors.New("tls: certificate verification failed"), false, "HEALTHY", 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			mockRoutingKubectl(t, `{"items":[]}`, routingProxyFixture("Valid", "contour.example"), "", false, false)
			requests := 0
			client := &http.Client{Transport: routingTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				if test.err != nil {
					return nil, test.err
				}
				header := make(http.Header)
				if test.code == 302 {
					header.Set("Location", "https://contour.example/unreachable")
				}
				return &http.Response{StatusCode: test.code, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			check := checkViyaHTTPRouting(client, "viya", "", ViyaAPIConfig{Enabled: true, BaseURL: "https://fallback.example"}, "token", test.strict)
			if check.Status != test.status || check.Routing.FailedRoutes != test.failed || check.Routing.AuthRestrictedRoutes != test.auth || requests != 1 {
				t.Fatalf("unexpected response result: %+v, summary %+v, requests %d", check, check.Routing, requests)
			}
		})
	}
}

func TestHTTPProxyCrossNamespaceInclude(t *testing.T) {
	root := `{"items":[{"metadata":{"name":"root","namespace":"viya"},"spec":{"virtualhost":{"fqdn":"contour.example"},"includes":[{"name":"child","namespace":"shared","conditions":[{"prefix":"/parent"}]}]},"status":{"currentStatus":"Valid"}}]}`
	child := `{"metadata":{"name":"child","namespace":"shared"},"spec":{"routes":[{"conditions":[{"prefix":"/child"}],"services":[{"name":"backend","port":8080}]}],"includes":[{"name":"root","namespace":"viya"}]},"status":{"currentStatus":"Valid"}}`
	mockRoutingKubectl(t, `{"items":[]}`, root, child, false, false)
	proxies, err := fetchHTTPProxies("", "viya")
	if err != nil {
		t.Fatal(err)
	}
	targets, valid, _, _ := resolveHTTPProxyRoutes(proxies)
	if len(proxies) != 2 || valid != 2 || len(targets) != 1 || targets[0].Path != "/parent/child" || targets[0].Namespace != "shared" || targets[0].Services[0].Port != 8080 {
		t.Fatalf("unexpected includes: proxies %+v, targets %+v", proxies, targets)
	}
}

func TestHTTPProxyMissingInclude(t *testing.T) {
	root := `{"items":[{"metadata":{"name":"root","namespace":"viya"},"spec":{"virtualhost":{"fqdn":"contour.example"},"includes":[{"name":"missing"}]},"status":{"currentStatus":"Valid"}}]}`
	mockRoutingKubectl(t, `{"items":[]}`, root, "", false, false)
	check := checkViyaHTTPRouting(&http.Client{}, "viya", "", ViyaAPIConfig{Enabled: true, BaseURL: "https://fallback.example"}, "", false)
	if check.Status != "DEGRADED" || len(check.Routing.DiscoveryErrors) == 0 || check.ErrorDetail == "" {
		t.Fatalf("missing include must be visible: %+v", check)
	}
}

// TestHTTPRoutingFailureThreshold locks in the "more than N broken routes is
// UNHEALTHY, otherwise HEALTHY (endpoints still listed)" behavior, with the
// threshold overridden via MaxFailedRoutes to keep the fixture small.
func TestHTTPRoutingFailureThreshold(t *testing.T) {
	ingress := `{"items":[{"metadata":{"name":"ingress"},"spec":{"rules":[{"host":"ingress.example","http":{"paths":[{"path":"/a"},{"path":"/b"},{"path":"/c"}]}}]}}]}`
	mockRoutingKubectl(t, ingress, `{"items":[]}`, "", false, false)
	client := &http.Client{Transport: routingTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	withinTolerance := checkViyaHTTPRouting(client, "viya", "", ViyaAPIConfig{Enabled: true, BaseURL: "http://fallback.example/", MaxFailedRoutes: 3}, "", false)
	if withinTolerance.Status != "HEALTHY" || withinTolerance.Routing.FailedRoutes != 3 {
		t.Fatalf("3 failures at threshold 3 must stay HEALTHY: %+v", withinTolerance)
	}

	beyondTolerance := checkViyaHTTPRouting(client, "viya", "", ViyaAPIConfig{Enabled: true, BaseURL: "http://fallback.example/", MaxFailedRoutes: 2}, "", false)
	if beyondTolerance.Status != "UNHEALTHY" {
		t.Fatalf("3 failures over threshold 2 must be UNHEALTHY: %+v", beyondTolerance)
	}
}

func TestResolveHTTPProxyRoutes(t *testing.T) {
	var list httpProxyList
	err := json.Unmarshal([]byte(`{"items":[
		{"metadata":{"name":"root","namespace":"viya"},"spec":{"virtualhost":{"fqdn":""},"routes":[{"conditions":[{"prefix":"/"}]}],"includes":[{"name":"child","conditions":[{"prefix":"/first"}]},{"name":"child","conditions":[{"prefix":"/second"}]}]},"status":{"currentStatus":"Valid"}},
		{"metadata":{"name":"child","namespace":"viya"},"spec":{"routes":[{"conditions":[{"prefix":"/api"}]}],"includes":[{"name":"root"}]},"status":{"currentStatus":"Valid"}}
	]}`), &list)
	if err != nil {
		t.Fatal(err)
	}
	targets, valid, invalid, orphaned := resolveHTTPProxyRoutes(list.Items)
	if valid != 2 || invalid != 0 || orphaned != 0 {
		t.Fatalf("unexpected status counts: %d %d %d", valid, invalid, orphaned)
	}
	want := []string{"/", "/first/api", "/second/api"}
	if len(targets) != len(want) {
		t.Fatalf("got routes %#v, want %v", targets, want)
	}
	for index, target := range targets {
		if target.Path != want[index] || target.Host != "" {
			t.Errorf("route %d: %#v", index, target)
		}
	}
}
