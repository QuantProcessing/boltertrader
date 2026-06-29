package okx

import (
	"encoding/json"
	"testing"
)

func TestTypes_BaseResponseDecode(t *testing.T) {
	var res BaseResponse[Balance]
	if err := json.Unmarshal([]byte(`{"code":"0","msg":"","data":[{"totalEq":"100"}]}`), &res); err != nil {
		t.Fatalf("decode base response: %v", err)
	}
	if res.Code != "0" || len(res.Data) != 1 || res.Data[0].TotalEq != "100" {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestAccountConfig_AccountLevel(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want AccountLevel
	}{
		{name: "simple", raw: "1", want: AccountLevelSimple},
		{name: "single currency margin", raw: "2", want: AccountLevelSingleCurrencyMargin},
		{name: "multi currency margin", raw: "3", want: AccountLevelMultiCurrencyMargin},
		{name: "portfolio margin", raw: "4", want: AccountLevelPortfolioMargin},
		{name: "unknown", raw: "9", want: AccountLevelUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AccountConfig{AcctLv: tc.raw}
			if got := cfg.AccountLevel(); got != tc.want {
				t.Fatalf("AccountLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}
