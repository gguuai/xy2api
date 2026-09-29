package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"net/http"
	"strings"
)

// Sub2APISchedulingEnabled tests the request snapshot, never a mutable global.
func Sub2APISchedulingEnabled(ctx context.Context) bool {
	r := controlledRequest(ctx)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.modeResolved && r.Mode.Mode == scheduling.ModeSub2API
}

// Auxiliary endpoints use group allocation and admission, but never consume
// generation concurrency, generation retry budgets, or recovery probes.
func schedulingAuxiliaryRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	path := req.URL.Path
	return strings.HasSuffix(path, "/count_tokens") || strings.HasSuffix(path, "/input_tokens") || strings.HasSuffix(path, ":countTokens") || (req.Method == http.MethodGet && (strings.HasSuffix(path, "/models") || strings.Contains(path, "/models/")))
}
func controlledAuxiliaryRequest(ctx context.Context) bool {
	r := controlledRequest(ctx)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Auxiliary
}
