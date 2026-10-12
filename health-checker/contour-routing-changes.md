# Contour HTTPProxy Routing Support

## Integration

The implementation is in `viya_health_checker.go`. Replace the declarations and functions below in the existing standalone health-checker module. No new imports or dependencies are required. The functions reuse the existing kubectlJSON, viyaRequest, authentication, exclusion, and console helpers; this is not a standalone replacement for the entire file.

The check is named **HTTP Routing Endpoints**. Ingress and HTTPProxy discovery run together. HTTPProxy includes are resolved recursively, including references to other namespaces using namespace-scoped GETs; the Kubernetes identity needs permission to read those referenced resources. Counts include resolved external children. A recursion-stack guard prevents circular includes without dropping repeated children under different prefixes. Routes are probed once per source/host/path.

HTTPProxy FQDNs always use HTTPS; missing FQDNs use the configured Viya URL. Root routes preserve /. Proxy metadata, virtualhost, conditions, includes, services (name/port), currentStatus, and description are retained in the JSON resource details. Route results retain URL, resource identity, services, HTTP status, health status, and errors.

Valid proxies are healthy; Invalid/Orphaned proxies make the aggregate check unhealthy even when no routes exist. Empty/unrecognized status degrades the check. Missing CRDs are tolerated. Other discovery failures degrade otherwise healthy results without preventing available routes from being probed. If no resources expose routes, the check is degraded with a clear message. Invalid/Orphaned status takes precedence over an empty-route result.

HTTP 200-399 is healthy, including redirects without following them. HTTP 401/403 is reachable and informational by default. The existing explicit `--strict-routing` opt-in still degrades the aggregate for authentication-restricted routes. HTTP 404 and other non-authentication 4xx responses are degraded; 5xx, connection/TLS errors, and response-body read errors are unhealthy. Routes can be probed without an authentication token if SASLogon authentication fails; authenticated API checks are still skipped in that case. The existing API-enabled configuration gate remains unchanged.

## JSON Changes

Under the existing `routing_summary` object, the following fields are added (including zero values):

```json
{
  "controller_type": "contour",
  "httpproxies_total": 12,
  "httpproxies_valid": 12,
  "httpproxies_invalid": 0,
  "httpproxies_orphaned": 0,
  "httpproxies_unknown": 0,
  "routes_tested": 63,
  "auth_restricted_routes": 0,
  "failed_routes": 0
}
```

Controller values are `ingress`, `contour`, `mixed`, or `none`. Existing routing_type, ingress_paths, httpproxy_routes, httpproxy_valid/invalid/orphaned, targets_probed, auth_issues, and failures fields are retained. Optional httpproxies, routes, and discovery_errors arrays provide diagnostics. failed_routes/failures count degraded or unhealthy probe outcomes, not resource-status failures or informational authentication responses.

## Console Changes

The displayViyaAPIResults function below replaces the compact routing lines with:

```text
HTTP Routing Endpoints
Controller Type : contour
HTTPProxies     : 12
Valid           : 12
Invalid         : 0
Orphaned        : 0
Unknown         : 0
Routes Tested   : 63
Auth Restricted : 0 (informational)
Failures        : 0
```

Each proxy's namespace/name, controller status, and description follow the counters. Degraded/unhealthy route details and discovery errors are also displayed within the existing console frame.

## Updated Structs

The existing ViyaAPICheck declaration is included to show exactly where routing_summary is attached.

```go
type RoutingSummary struct {
	RoutingType          string          `json:"routing_type"`
	IngressPaths         int             `json:"ingress_paths"`
	HTTPProxyRoutes      int             `json:"httpproxy_routes"`
	HTTPProxyValid       int             `json:"httpproxy_valid"`
	HTTPProxyInvalid     int             `json:"httpproxy_invalid"`
	HTTPProxyOrphaned    int             `json:"httpproxy_orphaned"`
	TargetsProbed        int             `json:"targets_probed"`
	AuthIssues           int             `json:"auth_issues"`
	Failures             int             `json:"failures"`
	ControllerType       string          `json:"controller_type"`
	HTTPProxiesTotal     int             `json:"httpproxies_total"`
	HTTPProxiesValid     int             `json:"httpproxies_valid"`
	HTTPProxiesInvalid   int             `json:"httpproxies_invalid"`
	HTTPProxiesOrphaned  int             `json:"httpproxies_orphaned"`
	HTTPProxiesUnknown   int             `json:"httpproxies_unknown"`
	RoutesTested         int             `json:"routes_tested"`
	AuthRestrictedRoutes int             `json:"auth_restricted_routes"`
	FailedRoutes         int             `json:"failed_routes"`
	HTTPProxies          []httpProxyItem `json:"httpproxies,omitempty"`
	Routes               []routingResult `json:"routes,omitempty"`
	DiscoveryErrors      []string        `json:"discovery_errors,omitempty"`
}
```

```go
type ViyaAPICheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	// Count has no omitempty: a genuine zero is the signal that detects a
	// lossy restore, and must not disappear from the JSON report.
	Count int `json:"count"`
	// CountSource records how Count was derived (count_field, paginated or
	// unknown) so a consumer can tell an authoritative count from an
	// approximation.
	CountSource string `json:"count_source,omitempty"`
	// ErrorDetail carries a sanitized, truncated diagnostic (e.g. a parsed
	// SAS error body) for checks that failed with a non-obvious cause.
	ErrorDetail    string             `json:"error_detail,omitempty"`
	ComputeContext *ComputeContextRef `json:"compute_context,omitempty"`
	Routing        *RoutingSummary    `json:"routing_summary,omitempty"`
}
```

```go
type routingTarget struct {
	Host      string
	Path      string
	Source    string // "ingress" or "httpproxy"
	Name      string
	Namespace string
	Services  []httpProxyService
}
```

```go
type httpProxyService struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}
```

```go
type routingResult struct {
	URL            string             `json:"url"`
	Source         string             `json:"source"`
	Name           string             `json:"name"`
	Namespace      string             `json:"namespace"`
	Services       []httpProxyService `json:"services,omitempty"`
	Status         string             `json:"status"`
	HTTPStatus     int                `json:"http_status,omitempty"`
	AuthRestricted bool               `json:"auth_restricted"`
	Message        string             `json:"message,omitempty"`
}
```

```go
type httpProxyCondition struct {
	Prefix string `json:"prefix"`
}
```

```go
type httpProxyList struct {
	Items []httpProxyItem `json:"items"`
}
```

```go
type httpProxyItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		VirtualHost *struct {
			FQDN string `json:"fqdn"`
		} `json:"virtualhost"`
		Routes []struct {
			Conditions []httpProxyCondition `json:"conditions"`
			Services   []httpProxyService   `json:"services"`
		} `json:"routes"`
		Includes []struct {
			Name       string               `json:"name"`
			Namespace  string               `json:"namespace"`
			Conditions []httpProxyCondition `json:"conditions"`
		} `json:"includes"`
	} `json:"spec"`
	Status struct {
		CurrentStatus string `json:"currentStatus"`
		Description   string `json:"description"`
	} `json:"status"`
}
```

## Changed Functions

Complete implementations, with unchanged surrounding functions omitted.

### checkViyaAPIHealth

```go
func checkViyaAPIHealth(cfg ViyaAPIConfig, namespace, kubeconfig string, strictRouting bool) ViyaAPIHealth {
	api := ViyaAPIHealth{BaseURL: cfg.BaseURL}

	if !cfg.Enabled {
		api.Status = "SKIPPED"
		api.Message = "Viya API validation skipped (no URL or credentials provided)"
		return api
	}
	api.Executed = true

	client, err := newViyaHTTPClient(cfg.InsecureSkipTLSVerify, cfg.CACertPath, cfg.APITimeout)
	if err != nil {
		api.Status = "UNHEALTHY"
		api.Message = "Unable to configure HTTPS client: " + err.Error()
		return api
	}

	printAPIProgress("Authenticating with SASLogon...")
	token, tokenCheck := viyaAuthenticate(client, cfg)
	printAPIOutcome(tokenCheck.Status)
	api.Checks = append(api.Checks, tokenCheck)
	printAPIProgress("Discovering HTTP routes...")
	routingCheck := checkViyaHTTPRouting(client, namespace, kubeconfig, cfg, token, strictRouting)
	printAPIOutcome(routingCheck.Status)
	api.Checks = append(api.Checks, routingCheck)
	if token == "" {
		api.Status = "UNHEALTHY"
		api.Message = "SASLogon authentication failed - authenticated API checks skipped"
		return api
	}

	printAPIProgress("Checking FQDN accessibility...")
	fqdnCheck := viyaSimpleGet(client, cfg.BaseURL, token, "FQDN Accessibility", "")
	printAPIOutcome(fqdnCheck.Status)
	api.Checks = append(api.Checks, fqdnCheck)

	printAPIProgress("Checking compute contexts...")
	contexts, c := viyaCountCollection(client, cfg.BaseURL, token, "Compute Contexts", "/compute/contexts")
	printAPIOutcome(c.Status)
	api.Inventory.ComputeContexts = contexts
	api.Checks = append(api.Checks, c)

	api.Checks = append(api.Checks, viyaComputeSession(client, cfg.BaseURL, token, cfg.Username))

	printAPIProgress("Checking CAS, CASLIBs...")
	servers, caslibs, casChecks := viyaCASValidation(client, cfg.BaseURL, token)
	printAPIOutcome(worstStatus(checkStatuses(casChecks)...))
	api.Inventory.CASServers = servers
	api.Inventory.CASLibs = caslibs
	api.Checks = append(api.Checks, casChecks...)

	printAPIProgress("Checking Quality Knowledge Bases...")
	qkbs, c := viyaCountCollection(client, cfg.BaseURL, token, "Quality Knowledge Base", "/dataQuality/qkbs")
	printAPIOutcome(c.Status)
	api.Inventory.QKBs = qkbs
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking reports...")
	reports, c := viyaCountCollection(client, cfg.BaseURL, token, "Reports", "/reports/reports")
	printAPIOutcome(c.Status)
	api.Inventory.Reports = reports
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking folders...")
	folders, c := viyaCountCollection(client, cfg.BaseURL, token, "Folders", "/folders/folders")
	printAPIOutcome(c.Status)
	api.Inventory.Folders = folders
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking users...")
	users, c := viyaCountCollection(client, cfg.BaseURL, token, "Identities - Users", "/identities/users")
	printAPIOutcome(c.Status)
	api.Inventory.Users = users
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking groups...")
	groups, c := viyaCountCollection(client, cfg.BaseURL, token, "Identities - Groups", "/identities/groups")
	printAPIOutcome(c.Status)
	api.Inventory.Groups = groups
	api.Checks = append(api.Checks, c)

	failed, degraded := 0, 0
	for _, chk := range api.Checks {
		switch chk.Status {
		case "UNHEALTHY":
			failed++
		case "DEGRADED":
			degraded++
		}
	}

	switch {
	case failed > 0:
		api.Status = "UNHEALTHY"
		api.Message = fmt.Sprintf("%d Viya API check(s) failed, %d degraded", failed, degraded)
	case degraded > 0:
		api.Status = "DEGRADED"
		api.Message = fmt.Sprintf("%d Viya API check(s) degraded", degraded)
	default:
		api.Status = "HEALTHY"
		api.Message = "All SAS Viya application API checks passed"
	}

	return api
}
```

### fetchHTTPProxies

```go
// fetchHTTPProxies lists Contour HTTPProxy resources in namespace. A missing
// CRD is not an error - it simply yields an empty list so ingress-only
// clusters are unaffected.
func fetchHTTPProxies(kubeconfig, namespace string) ([]httpProxyItem, error) {
	var list httpProxyList
	if err := kubectlJSON(kubeconfig, &list, "get", "httpproxies.projectcontour.io", "-n", namespace); err != nil {
		if isMissingCRDError(err) {
			return nil, nil
		}
		return nil, err
	}
	known := make(map[string]bool)
	for _, item := range list.Items {
		known[item.Metadata.Namespace+"/"+item.Metadata.Name] = true
	}
	var discoveryErrors []error
	for index := 0; index < len(list.Items); index++ {
		item := list.Items[index]
		for _, include := range item.Spec.Includes {
			childNamespace := include.Namespace
			if childNamespace == "" {
				childNamespace = item.Metadata.Namespace
			}
			key := childNamespace + "/" + include.Name
			if known[key] {
				continue
			}
			known[key] = true
			if include.Name == "" {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("HTTPProxy %s/%s has an unnamed include", item.Metadata.Namespace, item.Metadata.Name))
				continue
			}
			var child httpProxyItem
			if err := kubectlJSON(kubeconfig, &child, "get", "httpproxies.projectcontour.io", include.Name, "-n", childNamespace); err != nil {
				discoveryErrors = append(discoveryErrors, err)
				continue
			}
			list.Items = append(list.Items, child)
		}
	}
	return list.Items, errors.Join(discoveryErrors...)
}
```

### resolveHTTPProxyRoutes

```go
// resolveHTTPProxyRoutes walks each root HTTPProxy (one that defines a
// virtualhost) and follows spec.includes[] recursively, combining parent and
// child prefixes, to build the full set of routable paths. It also tallies
// HTTPProxy resource status (Valid is healthy; Invalid/Orphaned is unhealthy).
func resolveHTTPProxyRoutes(items []httpProxyItem) (targets []routingTarget, valid, invalid, orphaned int) {
	byName := make(map[string]httpProxyItem, len(items))
	for _, it := range items {
		byName[it.Metadata.Namespace+"/"+it.Metadata.Name] = it
	}

	for _, it := range items {
		switch strings.ToLower(strings.TrimSpace(it.Status.CurrentStatus)) {
		case "valid":
			valid++
		case "invalid":
			invalid++
		case "orphaned":
			orphaned++
		}
	}

	for _, root := range items {
		if root.Spec.VirtualHost == nil {
			continue
		}

		visited := make(map[string]bool)
		walkHTTPProxy(root, root.Spec.VirtualHost.FQDN, "", byName, visited, &targets)
	}
	return targets, valid, invalid, orphaned
}
```

### walkHTTPProxy

```go
func walkHTTPProxy(item httpProxyItem, fqdn, parentPrefix string, byName map[string]httpProxyItem, visited map[string]bool, targets *[]routingTarget) {
	key := item.Metadata.Namespace + "/" + item.Metadata.Name
	if visited[key] {
		return
	}
	visited[key] = true
	defer delete(visited, key)

	for _, route := range item.Spec.Routes {
		prefix := combineRoutingPrefix(parentPrefix, firstPrefixCondition(route.Conditions))
		*targets = append(*targets, routingTarget{Host: fqdn, Path: prefix, Source: "httpproxy", Name: item.Metadata.Name, Namespace: item.Metadata.Namespace, Services: route.Services})
	}

	for _, inc := range item.Spec.Includes {
		ns := inc.Namespace
		if ns == "" {
			ns = item.Metadata.Namespace
		}
		child, ok := byName[ns+"/"+inc.Name]
		if !ok {

			continue
		}
		childPrefix := combineRoutingPrefix(parentPrefix, firstPrefixCondition(inc.Conditions))
		walkHTTPProxy(child, fqdn, childPrefix, byName, visited, targets)
	}
}
```

### checkViyaHTTPRouting

```go
// checkViyaHTTPRouting discovers HTTP routes from both Kubernetes Ingress and
// Contour HTTPProxy resources and probes each one. It supports Ingress,
// HTTPProxy, or both, so clusters that use Contour instead of an Ingress
// controller are still validated instead of reporting "no paths found".
// 401/403 responses are informational (a non-admin test user is expected to
// be denied some paths) unless strictRouting is set, and every discovered
// path is probed against its own host, falling back to cfg.BaseURL.
func checkViyaHTTPRouting(client *http.Client, namespace, kubeconfig string, cfg ViyaAPIConfig, token string, strictRouting bool) ViyaAPICheck {
	check := ViyaAPICheck{Name: "HTTP Routing Endpoints"}

	if !cfg.Enabled {
		check.Status = "SKIPPED"
		check.Message = "HTTP routing validation skipped (Viya API checks disabled)"
		return check
	}

	exclusions := loadExclusions()
	seen := make(map[string]bool)
	var targets []routingTarget

	var ingresses ingressList
	ingressErr := kubectlJSON(kubeconfig, &ingresses, "get", "ingress", "-n", namespace)
	if ingressErr == nil {
		for _, ing := range ingresses.Items {
			if isExcluded(ing.Metadata.Name, exclusions) {
				continue
			}
			for _, rule := range ing.Spec.Rules {
				for _, p := range rule.HTTP.Paths {
					path := p.Path

					if idx := strings.Index(path, "("); idx > 0 {
						path = path[:idx]
					}
					path = normalizeRoutingPrefix(path)
					key := "ingress|" + rule.Host + "|" + path
					if seen[key] {
						continue
					}
					seen[key] = true
					targets = append(targets, routingTarget{Host: rule.Host, Path: path, Source: "ingress", Name: ing.Metadata.Name, Namespace: namespace})
				}
			}
		}
	}
	ingressCount := len(targets)

	proxies, proxyErr := fetchHTTPProxies(kubeconfig, namespace)
	var httpProxyValid, httpProxyInvalid, httpProxyOrphaned int
	if len(proxies) > 0 {
		proxyTargets, valid, invalid, orphaned := resolveHTTPProxyRoutes(proxies)
		httpProxyValid, httpProxyInvalid, httpProxyOrphaned = valid, invalid, orphaned
		for _, t := range proxyTargets {
			key := "httpproxy|" + t.Host + "|" + t.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, t)
		}
	}
	httpProxyRouteCount := len(targets) - ingressCount

	routingType := "none"
	controllerType := "none"
	switch {
	case len(ingresses.Items) > 0 && len(proxies) > 0:
		routingType = "both"
		controllerType = "mixed"
	case len(ingresses.Items) > 0:
		routingType = "ingress"
		controllerType = "ingress"
	case len(proxies) > 0:
		routingType = "httpproxy"
		controllerType = "contour"
	}

	summary := &RoutingSummary{
		RoutingType:         routingType,
		IngressPaths:        ingressCount,
		HTTPProxyRoutes:     httpProxyRouteCount,
		HTTPProxyValid:      httpProxyValid,
		HTTPProxyInvalid:    httpProxyInvalid,
		HTTPProxyOrphaned:   httpProxyOrphaned,
		ControllerType:      controllerType,
		HTTPProxiesTotal:    len(proxies),
		HTTPProxiesValid:    httpProxyValid,
		HTTPProxiesInvalid:  httpProxyInvalid,
		HTTPProxiesOrphaned: httpProxyOrphaned,
		HTTPProxiesUnknown:  len(proxies) - httpProxyValid - httpProxyInvalid - httpProxyOrphaned,
		HTTPProxies:         proxies,
	}
	check.Routing = summary
	for _, err := range []error{ingressErr, proxyErr} {
		if err != nil {
			summary.DiscoveryErrors = append(summary.DiscoveryErrors, err.Error())
		}
	}

	scheme := "https"
	if u, parseErr := url.Parse(cfg.BaseURL); parseErr == nil && u.Scheme != "" {
		scheme = u.Scheme
	}

	probeClient := *client
	probeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	failures, authIssues, unhealthyRoutes := 0, 0, 0

	for _, t := range targets {
		target := strings.TrimRight(cfg.BaseURL, "/") + t.Path

		if t.Host != "" {
			routeScheme := scheme
			if t.Source == "httpproxy" {
				routeScheme = "https"
			}
			target = routeScheme + "://" + t.Host + t.Path
		}

		result := routingResult{URL: target, Source: t.Source, Name: t.Name, Namespace: t.Namespace, Services: t.Services}
		resp, err := viyaRequest(&probeClient, http.MethodGet, target, token, "", nil)
		if err != nil {
			failures++
			unhealthyRoutes++
			result.Status = "UNHEALTHY"
			result.Message = err.Error()
			summary.Routes = append(summary.Routes, result)
			continue
		}
		_, bodyErr := io.Copy(io.Discard, resp.Body)
		code := resp.StatusCode
		resp.Body.Close()
		result.HTTPStatus = code

		switch {
		case bodyErr != nil:
			failures++
			unhealthyRoutes++
			result.Status = "UNHEALTHY"
			result.Message = bodyErr.Error()
		case code >= 200 && code <= 399:
			result.Status = "HEALTHY"
		case code == 401 || code == 403:

			authIssues++
			result.Status = "HEALTHY"
			result.AuthRestricted = true
			result.Message = "Reachable (authentication restricted; informational)"
		case code >= 500 && code <= 599:
			failures++
			unhealthyRoutes++
			result.Status = "UNHEALTHY"
		default:
			failures++
			result.Status = "DEGRADED"
		}
		summary.Routes = append(summary.Routes, result)
	}

	summary.TargetsProbed = len(targets)
	summary.AuthIssues = authIssues
	summary.Failures = failures
	summary.RoutesTested = len(targets)
	summary.AuthRestrictedRoutes = authIssues
	summary.FailedRoutes = failures
	check.Count = len(targets)

	invalidOrOrphaned := httpProxyInvalid + httpProxyOrphaned
	switch {
	case unhealthyRoutes > 0 || invalidOrOrphaned > 0:
		check.Status = "UNHEALTHY"
		check.Message = fmt.Sprintf("%d of %d route(s) failed (%d auth-restricted, %d invalid/orphaned HTTPProxy)",
			failures, len(targets), authIssues, invalidOrOrphaned)
	case len(targets) == 0:
		check.Status = "DEGRADED"
		check.Message = "No Ingress or HTTPProxy routes found to validate; neither resource type exposes a route"
	case failures > 0 || summary.HTTPProxiesUnknown > 0 || len(summary.DiscoveryErrors) > 0:
		check.Status = "DEGRADED"
		check.Message = fmt.Sprintf("%d of %d route(s) failed, %d HTTPProxy status(es) unknown, %d discovery error(s)", failures, len(targets), summary.HTTPProxiesUnknown, len(summary.DiscoveryErrors))
	case authIssues > 0 && strictRouting:
		check.Status = "DEGRADED"
		check.Message = fmt.Sprintf("All %d route(s) reachable, %d returned 401/403 (--strict-routing)",
			len(targets), authIssues)
	case authIssues > 0:
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("All %d route(s) reachable, %d returned 401/403 (informational)",
			len(targets), authIssues)
	default:
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("All %d route(s) reachable (%s routing)", len(targets), routingType)
	}
	if len(summary.DiscoveryErrors) > 0 {
		check.ErrorDetail = strings.Join(summary.DiscoveryErrors, "; ")
	}

	return check
}
```

### displayViyaAPIResults

```go
func displayViyaAPIResults(api ViyaAPIHealth) {
	fmt.Printf("%s%s┌─ VIYA APPLICATION CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 53), ColorReset)

	color := getStatusColor(api.Status)
	symbol := getStatusSymbol(api.Status)

	fmt.Printf("│ %s Status:%s   %s%s %s%s\n", ColorBold, ColorReset, color, symbol, api.Status, ColorReset)
	fmt.Printf("│ %s URL:%s      %s\n", ColorBold, ColorReset, api.BaseURL)
	fmt.Printf("│ %s Message:%s  %s\n", ColorBold, ColorReset, api.Message)

	if len(api.Checks) > 0 {
		fmt.Printf("│\n")
		fmt.Printf("│ %s API Checks:%s\n", ColorBold, ColorReset)
		for _, c := range api.Checks {
			cColor := getStatusColor(c.Status)
			cSymbol := getStatusSymbol(c.Status)
			fmt.Printf("│   %s%s%s %-32s %s\n", cColor, cSymbol, ColorReset, c.Name, c.Message)
			if c.Routing != nil {
				r := c.Routing
				fmt.Printf("│       Controller Type : %s\n", r.ControllerType)
				fmt.Printf("│       HTTPProxies     : %d\n", r.HTTPProxiesTotal)
				fmt.Printf("│       Valid           : %d\n", r.HTTPProxiesValid)
				fmt.Printf("│       Invalid         : %d\n", r.HTTPProxiesInvalid)
				fmt.Printf("│       Orphaned        : %d\n", r.HTTPProxiesOrphaned)
				fmt.Printf("│       Unknown         : %d\n", r.HTTPProxiesUnknown)
				fmt.Printf("│       Routes Tested   : %d\n", r.RoutesTested)
				fmt.Printf("│       Auth Restricted : %d (informational)\n", r.AuthRestrictedRoutes)
				fmt.Printf("│       Failures        : %d\n", r.FailedRoutes)
				for _, proxy := range r.HTTPProxies {
					fmt.Printf("│       HTTPProxy %s/%s: %s - %s\n", proxy.Metadata.Namespace, proxy.Metadata.Name, proxy.Status.CurrentStatus, proxy.Status.Description)
				}
				for _, route := range r.Routes {
					if route.Status != "HEALTHY" {
						fmt.Printf("│       %s %s (HTTP %d) %s\n", route.Status, route.URL, route.HTTPStatus, route.Message)
					}
				}
			}
			if c.ComputeContext != nil {
				fmt.Printf("│       compute context: %s (%s)\n", c.ComputeContext.Name, c.ComputeContext.ID)
			}
			if c.ErrorDetail != "" {
				fmt.Printf("│       error detail: %s\n", c.ErrorDetail)
			}
		}

		fmt.Printf("│\n")
		fmt.Printf("│ %s Content Inventory (restore baseline):%s\n", ColorBold, ColorReset)
		fmt.Printf("│   Compute contexts: %d\n", api.Inventory.ComputeContexts)
		fmt.Printf("│   CAS servers:      %d\n", api.Inventory.CASServers)
		fmt.Printf("│   CASLIBs:          %d\n", api.Inventory.CASLibs)
		fmt.Printf("│   QKBs:             %d\n", api.Inventory.QKBs)
		fmt.Printf("│   Reports:          %d\n", api.Inventory.Reports)
		fmt.Printf("│   Folders:          %d\n", api.Inventory.Folders)
		fmt.Printf("│   Users:            %d\n", api.Inventory.Users)
		fmt.Printf("│   Groups:           %d\n", api.Inventory.Groups)
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}
```

## Required Existing Helpers

These remain unchanged and are included for copy-ready integration. No additional helper functions are needed.

### isMissingCRDError

```go
// isMissingCRDError reports whether a kubectl error indicates the resource
// type/CRD simply isn't registered on the cluster (not using Contour), as
// opposed to a real failure that should be surfaced.
func isMissingCRDError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "doesn't have a resource type") ||
		strings.Contains(msg, "the server could not find the requested resource") ||
		strings.Contains(msg, "no matches for kind")
}
```

### normalizeRoutingPrefix

```go
// normalizeRoutingPrefix ensures a path starts with "/" and has no trailing
// slash, while preserving "/" itself as a valid path (a bare TrimSuffix would
// otherwise reduce it to "").
func normalizeRoutingPrefix(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
```

### combineRoutingPrefix

```go
// combineRoutingPrefix joins a parent HTTPProxy's prefix with a child
// route/include prefix, the same way Contour composes prefixes when
// resolving spec.includes.
func combineRoutingPrefix(parent, child string) string {
	if child == "" {
		return normalizeRoutingPrefix(parent)
	}
	if parent == "" || parent == "/" {
		return normalizeRoutingPrefix(child)
	}
	return normalizeRoutingPrefix(strings.TrimSuffix(parent, "/") + "/" + strings.TrimPrefix(child, "/"))
}
```

### firstPrefixCondition

```go
func firstPrefixCondition(conditions []httpProxyCondition) string {
	for _, c := range conditions {
		if c.Prefix != "" {
			return c.Prefix
		}
	}
	return ""
}
```

## Verification

The routing_test.go tests cover Ingress-only/Contour-only/mixed discovery, missing CRDs, empty routing, proxy statuses, empty-FQDN fallback, JSON fields, repeated includes, cycles, cross-namespace children, missing includes, HTTP response classifications, connection/TLS errors, redirects, and the existing strict-routing opt-in. All tests use mocked Kubernetes discovery and controlled transports; no live cluster was used.
