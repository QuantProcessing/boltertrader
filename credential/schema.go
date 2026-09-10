package credential

type Field struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Metadata  bool   `json:"metadata,omitempty"`
	Required  bool   `json:"required"`
	ValueType string `json:"valueType"`
}

type Schema struct {
	SchemaVersion int     `json:"schemaVersion"`
	AuthKind      string  `json:"authKind"`
	Fields        []Field `json:"fields"`
}

// Schema describes the fields accepted by Decode. It contains no credentials
// and returns owned fields so consumers cannot mutate the registry.
func (registry *Registry) Schema(venue string) (Schema, error) {
	if registry == nil {
		return Schema{}, invalidBundle("unknown venue")
	}
	kind, ok := registry.authKinds[venue]
	fields, known := schemaFields[kind]
	if !ok || !known {
		return Schema{}, invalidBundle("unknown venue")
	}
	return Schema{SchemaVersion: 1, AuthKind: string(kind), Fields: append([]Field(nil), fields...)}, nil
}

var schemaFields = map[AuthKind][]Field{
	AuthAPIKeySecret:           {{Key: "apiKey", Label: "API key", Required: true, ValueType: "string"}, {Key: "apiSecret", Label: "API secret", Required: true, ValueType: "string"}},
	AuthAPIKeySecretPassphrase: {{Key: "apiKey", Label: "API key", Required: true, ValueType: "string"}, {Key: "apiSecret", Label: "API secret", Required: true, ValueType: "string"}, {Key: "passphrase", Label: "Passphrase", Required: true, ValueType: "string"}},
	AuthLighterSigner:          {{Key: "accountIndex", Label: "Account index", Metadata: true, Required: true, ValueType: "integer"}, {Key: "keyIndex", Label: "Key index", Metadata: true, Required: true, ValueType: "integer"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}},
	AuthEVMPrivateKey:          {{Key: "accountAddress", Label: "Account address", Metadata: true, ValueType: "string"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}},
	AuthAsterAPIWallet:         {{Key: "userAddress", Label: "User address", Metadata: true, Required: true, ValueType: "string"}, {Key: "expectedSigner", Label: "Expected signer", Metadata: true, ValueType: "string"}, {Key: "apiWalletPrivateKey", Label: "API wallet private key", Required: true, ValueType: "string"}},
	AuthNadoSigner:             {{Key: "subaccount", Label: "Subaccount", Metadata: true, Required: true, ValueType: "string"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}},
}
