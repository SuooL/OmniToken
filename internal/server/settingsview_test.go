package server

import "testing"

func TestValidateCurrencyAcceptsOnlyUSDAndCNY(t *testing.T) {
	valid := []currencySetting{
		{Code: "USD", Rate: 1},
		{Code: "CNY", Rate: 7.2},
		{Code: "cny", Rate: 7.2}, // case is normalised before checking
		{Code: " USD ", Rate: 1}, // surrounding space is trimmed
	}
	for _, c := range valid {
		if err := validateCurrency(c); err != nil {
			t.Errorf("validateCurrency(%+v) = %v, want nil", c, err)
		}
	}

	invalid := []struct {
		name string
		cur  currencySetting
	}{
		// Previously any 3-letter code passed, promising a conversion the
		// project cannot do — there is no FX feed behind it.
		{"unsupported code", currencySetting{Code: "EUR", Rate: 0.9}},
		{"not a currency", currencySetting{Code: "XYZ", Rate: 2}},
		{"empty", currencySetting{Code: "", Rate: 1}},
		// USD is the storage currency; any other rate would silently scale
		// every amount on every page.
		{"USD with non-1 rate", currencySetting{Code: "USD", Rate: 7.2}},
		{"zero rate", currencySetting{Code: "CNY", Rate: 0}},
		{"negative rate", currencySetting{Code: "CNY", Rate: -1}},
		{"rate above cap", currencySetting{Code: "CNY", Rate: maxCurrencyRate + 1}},
	}
	for _, tc := range invalid {
		if err := validateCurrency(tc.cur); err == nil {
			t.Errorf("%s: validateCurrency(%+v) = nil, want an error", tc.name, tc.cur)
		}
	}
}
