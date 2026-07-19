package factory

import "net/http"

type settings struct {
	endpoint          string
	webSocketEndpoint string
	httpClient        *http.Client
	environment       Environment
	accountAddress    string
	err               error
}

func newSettings(options []Option) settings {
	var settings settings
	for _, option := range options {
		if option.apply == nil {
			settings.err = invalidConfig("unknown factory option")
			return settings
		}
		if err := option.apply(&settings); err != nil {
			settings.err = err
			return settings
		}
	}
	return settings
}

func (settings settings) validate() error {
	if settings.err != nil {
		return settings.err
	}
	if settings.environment == "" {
		return invalidConfig("environment is required")
	}
	return nil
}
