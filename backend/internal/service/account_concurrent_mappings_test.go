//go:build unit

package service

import (
	"sync"
	"testing"
)

func concurrentDerivedMappingAccount() *Account {
	return &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"model_mapping":              map[string]any{"client": "upstream", "model-b": "upstream-b"},
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides:       map[string]any{"X-Custom-Client": "fixture"},
	}}
}

func TestAccountDerivedMappingsConcurrentReadAndCopy(t *testing.T) {
	account := concurrentDerivedMappingAccount()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for j := 0; j < 100; j++ {
				if got := account.GetModelMapping()["client"]; got != "upstream" {
					t.Errorf("model mapping = %q", got)
				}
				if got := account.GetHeaderOverrides()["x-custom-client"]; got != "fixture" {
					t.Errorf("header override = %q", got)
				}
				copied := *account
				if got := copied.GetModelMapping()["client"]; got != "upstream" {
					t.Errorf("copied mapping = %q", got)
				}
				if got := copied.GetHeaderOverrides()["x-custom-client"]; got != "fixture" {
					t.Errorf("copied header = %q", got)
				}
			}
		}()
	}
	close(start)
	workers.Wait()
}

func BenchmarkAccountDerivedMappingsRead(b *testing.B) {
	account := concurrentDerivedMappingAccount()
	account.GetModelMapping()
	account.GetHeaderOverrides()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if account.GetModelMapping()["client"] != "upstream" {
			b.Fatal("wrong model")
		}
		if account.GetHeaderOverrides()["x-custom-client"] != "fixture" {
			b.Fatal("wrong header")
		}
	}
}
