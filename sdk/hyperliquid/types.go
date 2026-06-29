package hyperliquid

type Tif string

const (
	TifGtc Tif = "Gtc"
	TifIoc Tif = "Ioc"
	TifFok Tif = "Fok"
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
	Status   string `json:"status"`
	Response *struct {
		Type string `json:"type"`
		Data T      `json:"data"`
	} `json:"response"`
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
