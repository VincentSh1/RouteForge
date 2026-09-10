package observability

import (
	"strings"
	"testing"
)

func TestConsoleUsesReadOnlySameOriginProxy(t *testing.T) {
	proxy := readDeploymentFile(t, "../../web/nginx.conf")
	for _, required := range []string{
		"set $upstream routeforge:8081", "rewrite ^/api/(.*)$ /admin/v1/$1 break",
		"proxy_pass http://$upstream", "resolver 127.0.0.11",
		"proxy_pass_request_headers off", "proxy_pass_request_body off",
		"return 405", "location /api/ { return 404; }",
		"try_files $uri /index.html", "Cache-Control \"no-store\"",
		"access_log off", "frame-ancestors 'none'",
	} {
		if !strings.Contains(proxy, required) {
			t.Errorf("console proxy is missing %q", required)
		}
	}
	if strings.Contains(proxy, "Access-Control-Allow-Origin") {
		t.Error("same-origin console must not add CORS")
	}
	dockerfile := readDeploymentFile(t, "../../web/Dockerfile")
	for _, required := range []string{"package-lock.json", "RUN npm ci", "RUN npm run build", "USER 101:101", "COPY --from=builder /app/dist"} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("console Dockerfile is missing %q", required)
		}
	}
	workflow := readDeploymentFile(t, "../../.github/workflows/go-ci.yml")
	for _, required := range []string{"working-directory: web", "npm ci", "npm run typecheck", "npm test", "npm run build"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("fast CI is missing %q", required)
		}
	}
}
