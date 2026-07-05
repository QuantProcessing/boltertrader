package hyperliquid

import "encoding/json"

type Tif string

const (
	TifGtc Tif = "Gtc"
	TifIoc Tif = "Ioc"
	TifFok Tif = "Fok"
	TifAlo Tif = "Alo"
)

type Side string

const (
	SideAsk Side = "A"
	SideBid Side = "B"
)

type Tpsl string

const (
	TakeProfit Tpsl = "tp"
	StopLoss   Tpsl = "sl"
)

type Grouping string

const (
	GroupingNA           Grouping = "na"
	GroupingNormalTpsl   Grouping = "normalTpsl"
	GroupingPositionTpls Grouping = "positionTpsl"
)

type APIResponse[T any] struct {
	Status   string              `json:"status"`
	Response *APIResponseBody[T] `json:"response"`
	Error    string              `json:"-"`
}

type APIResponseBody[T any] struct {
	Type string `json:"type"`
	Data T      `json:"data"`
}

func (r *APIResponse[T]) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Status = raw.Status
	if len(raw.Response) == 0 || string(raw.Response) == "null" {
		return nil
	}
	var message string
	if err := json.Unmarshal(raw.Response, &message); err == nil {
		r.Error = message
		return nil
	}
	var body APIResponseBody[T]
	if err := json.Unmarshal(raw.Response, &body); err != nil {
		return err
	}
	r.Response = &body
	return nil
}

func (r *APIResponse[T]) FailureMessage() string {
	if r == nil {
		return "unknown error"
	}
	if r.Error != "" {
		return r.Error
	}
	if r.Status != "" {
		return r.Status
	}
	return "unknown error"
}

type UserFees struct {
	DailyUserVlm []DailyUserVlm `json:"dailyUserVlm"`
	FeeSchedule  FeeSchedule    `json:"feeSchedule"`
}
type DailyUserVlm struct {
	Date      string `json:"date"`
	UserCross string `json:"userCross"`
	UserAdd   string `json:"userAdd"`
	Exchange  string `json:"exchange"`
}
type FeeSchedule struct {
	Cross                  string                `json:"cross"`
	Add                    string                `json:"add"`
	SpotCross              string                `json:"spotCross"`
	SpotAdd                string                `json:"spotAdd"`
	Tiers                  Tiers                 `json:"tiers"`
	ReferralDiscount       string                `json:"referralDiscount"`
	StakingDiscountTiers   []StakingDiscountTier `json:"stakingDiscountTiers"`
	UserCrossRate          string                `json:"userCrossRate"`
	UserAddRate            string                `json:"userAddRate"`
	UserSpotCrossRate      string                `json:"userSpotCrossRate"`
	UserSpotAddRate        string                `json:"userSpotAddRate"`
	ActiveReferralDiscount string                `json:"activeReferralDiscount"`
	FeeTrialReward         string                `json:"feeTrialReward"`
	StakingLink            StakingLink           `json:"stakingLink"`
	ActiveStakingDiscount  ActiveStakingDiscount `json:"activeStakingDiscount"`
}
type ActiveStakingDiscount struct {
	BpsOfMaxSupply string `json:"bpsOfMaxSupply"`
	Discount       string `json:"discount"`
}
type StakingLink struct {
	Type        string `json:"type"`
	StakingUser string `json:"stakingUser"`
}
type StakingDiscountTier struct {
	BpsOfMaxSupply string `json:"bpsOfMaxSupply"`
	Discount       string `json:"discount"`
}
type Tiers struct {
	Vip []Vip `json:"vip"`
	MM  []MM  `json:"mm"`
}
type Vip struct {
	NtlCutoff string `json:"ntlCutoff"`
	Cross     string `json:"cross"`
	Add       string `json:"add"`
	SpotCross string `json:"spotCross"`
	SpotAdd   string `json:"spotAdd"`
}
type MM struct {
	MakerFractionCutoff string `json:"makerFractionCutoff"`
	Add                 string `json:"add"`
}
