package commands

import (
	"github.com/RedwindA/ccusage_go/internal/pricing"
	"github.com/spf13/cobra"
)

// startPricing begins the pricing work that does not depend on usage data —
// decoding embedded tables and, when online, fetching live price lists — so
// it overlaps with loading. Pass the result to newPricingService.
func startPricing(cmd *cobra.Command, offline bool) *pricing.Prefetch {
	pricing.Preload()
	if offline {
		return nil
	}
	return pricing.StartPrefetch(cmd.Context())
}

func newPricingService(offline bool, prefetch *pricing.Prefetch) *pricing.Service {
	service := pricing.NewService()
	service.SetOffline(offline)
	if prefetch != nil {
		service.UsePrefetch(prefetch)
	}
	return service
}
