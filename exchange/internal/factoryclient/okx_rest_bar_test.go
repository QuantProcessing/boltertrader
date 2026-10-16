package factoryclient

import "testing"

func TestOKXRESTBarUsesVenueCase(t *testing.T) {
	for in, want := range map[string]string{"1m": "1m", "15m": "15m", "1h": "1H", "4h": "4H", "1d": "1D", "1H": "1H"} {
		if got := okxRESTBar(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
