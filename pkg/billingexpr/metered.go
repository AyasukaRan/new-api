package billingexpr

import "fmt"

const (
	BillingUnitPage      BillingUnit = "page"
	BillingUnitSecond    BillingUnit = "second"
	BillingUnitCharacter BillingUnit = "character"
)

// MeteredUsage represents non-token work; providers supply the final quantity.
type MeteredUsage struct {
	Unit  BillingUnit `json:"unit"`
	Count int         `json:"count"`
}

func (usage MeteredUsage) Variable() string {
	switch usage.Unit {
	case BillingUnitPage:
		return "document_pages"
	case BillingUnitSecond:
		return "audio_seconds"
	case BillingUnitCharacter:
		return "speech_characters"
	default:
		return ""
	}
}

func (usage MeteredUsage) Validate() error {
	limit := 0
	switch usage.Unit {
	case BillingUnitPage:
		limit = 1000
	case BillingUnitSecond:
		limit = 120
	case BillingUnitCharacter:
		limit = 10000
	default:
		return fmt.Errorf("unsupported metered billing unit %q", usage.Unit)
	}
	if usage.Count < 0 || usage.Count > limit {
		return fmt.Errorf("%s must be between 0 and %d", usage.Variable(), limit)
	}
	return nil
}
