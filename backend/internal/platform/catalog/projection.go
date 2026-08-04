package catalog

type Venue struct {
	Venue       string `json:"venue"`
	Environment string `json:"environment"`
	Ready       bool   `json:"ready"`
}

type Instrument struct {
	InstrumentID string `json:"instrumentId"`
	Venue        string `json:"venue"`
	Ready        bool   `json:"ready"`
}
