package lighter

import "testing"

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Code: 400, Message: "bad request"}

	if got := err.Error(); got != "API Error 400: bad request" {
		t.Fatalf("unexpected error string: %s", got)
	}
}

func TestAccountLimitsResponse_AccountTier(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want AccountTier
	}{
		{name: "standard", raw: "standard", want: AccountTierStandard},
		{name: "premium", raw: "premium", want: AccountTierPremium},
		{name: "plus", raw: "plus", want: AccountTierPlus},
		{name: "case insensitive", raw: "Premium", want: AccountTierPremium},
		{name: "unknown", raw: "enterprise", want: AccountTierUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AccountLimitsResponse{UserTier: tc.raw}.AccountTier()
			if got != tc.want {
				t.Fatalf("AccountTier() = %q, want %q", got, tc.want)
			}
		})
	}
}
