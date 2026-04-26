package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterRoutes_OpenAICompatibilityPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := &Handler{
		// 通过静态 key 触发鉴权路径，避免依赖数据库实例。
		configKeys: map[string]bool{"sk-test": true},
	}
	h.RegisterRoutes(r)

	cases := []struct {
		method       string
		path         string
		allowedCodes map[int]struct{}
	}{
		{method: http.MethodPost, path: "/v1/chat/completions", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/chat/completions", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/v1/responses", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/responses", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodGet, path: "/v1/responses", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/v1/responses/compact", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/responses/compact", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodGet, path: "/backend-api/codex/responses", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/backend-api/codex/responses", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/backend-api/codex/responses/compact", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/v1/images/generations", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/images/generations", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/v1/images/edits", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodPost, path: "/images/edits", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodGet, path: "/v1/models", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
		{method: http.MethodGet, path: "/models", allowedCodes: map[int]struct{}{http.StatusUnauthorized: {}}},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if _, ok := tc.allowedCodes[rec.Code]; !ok {
			t.Fatalf("%s %s status=%d, allowed=%v", tc.method, tc.path, rec.Code, tc.allowedCodes)
		}
	}
}
