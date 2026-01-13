package auth

import (
	"testing"
	"time"
)

func TestTierCache_GetSet(t *testing.T) {
	t.Parallel()

	cache := NewTierCache(time.Hour)

	// Test Get on empty cache
	result := cache.Get("auth-1")
	if result != nil {
		t.Error("Get on empty cache should return nil")
	}

	// Test Set and Get
	info := &TierInfo{
		IsPro:     true,
		TierID:    "pro-tier",
		TierName:  "Pro Plan",
		FetchedAt: time.Now(),
	}

	cache.Set("auth-1", info)

	result = cache.Get("auth-1")
	if result == nil {
		t.Fatal("Get after Set should return info")
	}

	if !result.IsPro {
		t.Error("IsPro should be true")
	}

	if result.TierID != "pro-tier" {
		t.Errorf("TierID = %q, want %q", result.TierID, "pro-tier")
	}

	if result.TierName != "Pro Plan" {
		t.Errorf("TierName = %q, want %q", result.TierName, "Pro Plan")
	}

	// Verify deep copy - modifying original shouldn't affect cached
	info.IsPro = false
	result2 := cache.Get("auth-1")
	if !result2.IsPro {
		t.Error("Cached info should not be affected by original modification")
	}

	// Verify deep copy - modifying returned shouldn't affect cached
	result.TierName = "Modified"
	result3 := cache.Get("auth-1")
	if result3.TierName != "Pro Plan" {
		t.Error("Cached info should not be affected by returned value modification")
	}
}

func TestTierCache_TTLExpiration(t *testing.T) {
	t.Parallel()

	// Create cache with very short TTL
	cache := NewTierCache(50 * time.Millisecond)

	info := &TierInfo{
		IsPro:     true,
		TierID:    "pro-tier",
		TierName:  "Pro Plan",
		FetchedAt: time.Now(),
	}

	cache.Set("auth-1", info)

	// Should be available immediately
	result := cache.Get("auth-1")
	if result == nil {
		t.Fatal("Get should return info before TTL expires")
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Should be nil after TTL expires
	result = cache.Get("auth-1")
	if result != nil {
		t.Error("Get should return nil after TTL expires")
	}
}

func TestTierCache_Delete(t *testing.T) {
	t.Parallel()

	cache := NewTierCache(time.Hour)

	// Set a tier info
	cache.Set("auth-1", &TierInfo{IsPro: true, FetchedAt: time.Now()})

	// Verify it exists
	if cache.Get("auth-1") == nil {
		t.Fatal("Info should exist after Set")
	}

	// Delete it
	cache.Delete("auth-1")

	// Verify it's gone
	if cache.Get("auth-1") != nil {
		t.Error("Info should be nil after Delete")
	}

	// Delete non-existent should not panic
	cache.Delete("non-existent")
}

func TestTierCache_Len(t *testing.T) {
	t.Parallel()

	cache := NewTierCache(time.Hour)

	if cache.Len() != 0 {
		t.Error("Empty cache should have length 0")
	}

	cache.Set("auth-1", &TierInfo{FetchedAt: time.Now()})
	cache.Set("auth-2", &TierInfo{FetchedAt: time.Now()})

	if cache.Len() != 2 {
		t.Errorf("Cache should have length 2, got %d", cache.Len())
	}

	cache.Delete("auth-1")

	if cache.Len() != 1 {
		t.Errorf("Cache should have length 1 after delete, got %d", cache.Len())
	}
}

func TestTierCache_IsExpired(t *testing.T) {
	t.Parallel()

	cache := NewTierCache(time.Hour)

	// Nil info is expired
	if !cache.IsExpired(nil) {
		t.Error("Nil info should be expired")
	}

	// Fresh info is not expired
	freshInfo := &TierInfo{FetchedAt: time.Now()}
	if cache.IsExpired(freshInfo) {
		t.Error("Fresh info should not be expired")
	}

	// Old info is expired
	oldInfo := &TierInfo{FetchedAt: time.Now().Add(-2 * time.Hour)}
	if !cache.IsExpired(oldInfo) {
		t.Error("Old info should be expired")
	}
}

func TestTierCache_NilCache(t *testing.T) {
	t.Parallel()

	var cache *TierCache

	// All operations on nil cache should not panic
	if cache.Get("auth-1") != nil {
		t.Error("Get on nil cache should return nil")
	}

	cache.Set("auth-1", &TierInfo{}) // Should not panic
	cache.Delete("auth-1")           // Should not panic

	if cache.Len() != 0 {
		t.Error("Len on nil cache should return 0")
	}

	if !cache.IsExpired(&TierInfo{}) {
		t.Error("IsExpired on nil cache should return true")
	}
}

func TestTierCache_DefaultTTL(t *testing.T) {
	t.Parallel()

	// Zero TTL should default to 1 hour
	cache := NewTierCache(0)
	if cache.ttl != time.Hour {
		t.Errorf("Default TTL should be 1 hour, got %v", cache.ttl)
	}

	// Negative TTL should default to 1 hour
	cache2 := NewTierCache(-time.Minute)
	if cache2.ttl != time.Hour {
		t.Errorf("Negative TTL should default to 1 hour, got %v", cache2.ttl)
	}
}

// ============================================
// IsPaidTier Tests
// ============================================

func TestIsPaidTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		info     *SubscriptionInfo
		expected bool
	}{
		{
			name:     "nil info",
			info:     nil,
			expected: false,
		},
		{
			name: "pro tier in currentTier",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "gemini-pro-tier"},
			},
			expected: true,
		},
		{
			name: "ultra tier in currentTier",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "gemini-ultra-plan"},
			},
			expected: true,
		},
		{
			name: "pro tier in paidTier (takes precedence)",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "free-tier"},
				PaidTier:    &SubscriptionTier{ID: "pro-subscription"},
			},
			expected: true,
		},
		{
			name: "free tier only",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "free-tier"},
			},
			expected: false,
		},
		{
			name: "empty tier ID",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: ""},
			},
			expected: false,
		},
		{
			name: "nil currentTier",
			info: &SubscriptionInfo{
				CurrentTier: nil,
			},
			expected: false,
		},
		{
			name: "case insensitive - PRO",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "GEMINI-PRO"},
			},
			expected: true,
		},
		{
			name: "case insensitive - Ultra",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "Gemini-Ultra"},
			},
			expected: true,
		},
		{
			name: "basic tier (not pro/ultra)",
			info: &SubscriptionInfo{
				CurrentTier: &SubscriptionTier{ID: "basic-tier"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPaidTier(tt.info)
			if result != tt.expected {
				t.Errorf("IsPaidTier() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestBuildTierInfoFromSubscription(t *testing.T) {
	t.Parallel()

	// Nil info
	result := BuildTierInfoFromSubscription(nil)
	if result != nil {
		t.Error("BuildTierInfoFromSubscription(nil) should return nil")
	}

	// Pro tier
	proInfo := &SubscriptionInfo{
		CurrentTier: &SubscriptionTier{
			ID:   "gemini-pro",
			Name: "Gemini Pro Plan",
		},
	}
	result = BuildTierInfoFromSubscription(proInfo)
	if result == nil {
		t.Fatal("BuildTierInfoFromSubscription should return non-nil for valid info")
	}
	if !result.IsPro {
		t.Error("IsPro should be true for pro tier")
	}
	if result.TierID != "gemini-pro" {
		t.Errorf("TierID = %q, want %q", result.TierID, "gemini-pro")
	}
	if result.TierName != "Gemini Pro Plan" {
		t.Errorf("TierName = %q, want %q", result.TierName, "Gemini Pro Plan")
	}
	if result.FetchedAt.IsZero() {
		t.Error("FetchedAt should be set")
	}

	// Free tier
	freeInfo := &SubscriptionInfo{
		CurrentTier: &SubscriptionTier{
			ID:   "free-tier",
			Name: "Free Plan",
		},
	}
	result = BuildTierInfoFromSubscription(freeInfo)
	if result == nil {
		t.Fatal("BuildTierInfoFromSubscription should return non-nil for valid info")
	}
	if result.IsPro {
		t.Error("IsPro should be false for free tier")
	}

	// PaidTier takes precedence
	mixedInfo := &SubscriptionInfo{
		CurrentTier: &SubscriptionTier{ID: "free-tier", Name: "Free"},
		PaidTier:    &SubscriptionTier{ID: "ultra-tier", Name: "Ultra Plan"},
	}
	result = BuildTierInfoFromSubscription(mixedInfo)
	if result == nil {
		t.Fatal("BuildTierInfoFromSubscription should return non-nil for valid info")
	}
	if !result.IsPro {
		t.Error("IsPro should be true when PaidTier is ultra")
	}
	if result.TierID != "ultra-tier" {
		t.Errorf("TierID should be from PaidTier, got %q", result.TierID)
	}
	if result.TierName != "Ultra Plan" {
		t.Errorf("TierName should be from PaidTier, got %q", result.TierName)
	}
}
