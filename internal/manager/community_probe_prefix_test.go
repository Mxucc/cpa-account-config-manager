package manager

import "testing"

// The prefix is the fallback that makes a community channel reachable at all: a
// plain model id such as "glm-5.3" is published by many channels, so CPA can route a
// request for it to somebody else's row. The prefixed name belongs to this provider.
func TestCommunityChannelPrefixAndCallableModel(t *testing.T) {
	if got := communityChannelPrefix("WorkBuddy"); got != "workbuddy" {
		t.Fatalf("prefix = %q", got)
	}
	if got := communityChannelPrefix("  zen-free  "); got != "zen-free" {
		t.Fatalf("prefix = %q", got)
	}
	// Anything that could break a model id becomes a dash.
	if got := communityChannelPrefix("a/b:c d"); got != "a-b-c-d" {
		t.Fatalf("prefix = %q", got)
	}
	if got := communityCallableModel("workbuddy", "glm-5.3"); got != "workbuddy/glm-5.3" {
		t.Fatalf("callable = %q", got)
	}
	if got := communityCallableModel("", "glm-5.3"); got != "glm-5.3" {
		t.Fatalf("callable = %q", got)
	}
}

// A routed failure and an upstream failure must not read the same: the hint decides
// what the operator does next.
func TestCommunityTestHintSeparatesTheTwoFailures(t *testing.T) {
	ok := communityTestResult{OK: true, Status: 200}
	bad := communityTestResult{OK: false, Status: 401}
	gatewayBad := communityTestResult{OK: false, Status: 503}

	both := communityTestHint(communityChannelTest{Upstream: bad, Routed: gatewayBad})
	if both == "" {
		t.Fatal("a total failure produced no hint")
	}
	upstreamOnly := communityTestHint(communityChannelTest{Upstream: ok, Routed: gatewayBad})
	if upstreamOnly == both {
		t.Fatal("a routed-only failure reads like a credential failure")
	}
	if communityTestHint(communityChannelTest{Upstream: ok, Routed: ok}) != "" {
		t.Fatal("a working channel produced a hint")
	}
}
