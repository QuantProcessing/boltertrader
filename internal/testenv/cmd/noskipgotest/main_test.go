package main

import "testing"

func TestScanResultCountsTestLevelSkips(t *testing.T) {
	var result scanResult
	result.observe([]byte(`{"Action":"run","Package":"p","Test":"TestLive"}`))
	result.observe([]byte(`{"Action":"output","Package":"p","Test":"TestLive","Output":"    live_test.go:1: skipping\n"}`))
	result.observe([]byte(`{"Action":"skip","Package":"p","Test":"TestLive"}`))
	result.observe([]byte(`{"Action":"pass","Package":"p"}`))
	if result.testEvents != 1 {
		t.Fatalf("testEvents=%d, want 1", result.testEvents)
	}
	if result.skips != 1 {
		t.Fatalf("skips=%d, want 1", result.skips)
	}
}

func TestScanResultIgnoresPackageLevelActions(t *testing.T) {
	var result scanResult
	result.observe([]byte(`{"Action":"pass","Package":"p"}`))
	if result.testEvents != 0 || result.skips != 0 {
		t.Fatalf("unexpected counters: %+v", result)
	}
}
