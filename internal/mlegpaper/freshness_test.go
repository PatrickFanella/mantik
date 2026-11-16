package mlegpaper

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocalAdmissionChecksUsedPriceAgeAtPOST(t *testing.T) {
	for _, age := range []time.Duration{24*time.Hour - time.Nanosecond, 24 * time.Hour, 24*time.Hour + time.Nanosecond, 39 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			o := admissionFixture(t, "iron_condor", true)
			now := o.Chain.AsOf.Add(22 * time.Hour)
			original := o.Chain.AsOf
			for i := range o.Chain.Entries {
				if o.Chain.Entries[i].Symbol == o.Structure.Legs[0].Symbol {
					o.Chain.Entries[i].PriceOccurredAt = now.Add(-age)
				}
			}
			if err := o.Chain.Validate(original, o.Chain.AssetID); err != nil {
				t.Fatal("invalid source fixture", err)
			}
			err := admit(context.Background(), o, now)
			if (err == nil) != (age <= o.Chain.Assumptions.MaxAge) {
				t.Fatalf("price age %s, max %s: %v", age, o.Chain.Assumptions.MaxAge, err)
			}
			if !o.Chain.AsOf.Equal(original) {
				t.Fatal("POST validation rewrote the retained source cutoff")
			}
		})
	}
}

func TestLocalAdmissionIgnoresUnusedPriceAge(t *testing.T) {
	o := admissionFixture(t, "iron_condor", true)
	for i := range o.Chain.Entries {
		if o.Chain.Entries[i].Symbol == "TST261218C00001000" {
			o.Chain.Entries[i].PriceOccurredAt = o.Chain.AsOf.Add(-17 * time.Hour)
		}
	}
	if err := admit(context.Background(), o, o.Chain.AsOf.Add(22*time.Hour)); err != nil {
		t.Fatal("untraded contract blocked fresh used evidence", err)
	}
}

func TestLocalAdmissionChecksExpiryAtPOST(t *testing.T) {
	o := admissionFixture(t, "iron_condor", true)
	o.Chain.AsOf = o.Chain.AsOf.Add(-90 * time.Minute)
	o.Chain.Spot.At = o.Chain.AsOf.Add(-time.Hour)
	o.Chain.Spot.ObservedAt, o.Chain.Spot.AvailableAt = o.Chain.AsOf, o.Chain.AsOf
	for i := range o.Chain.Entries {
		e := &o.Chain.Entries[i]
		e.Symbol = strings.ReplaceAll(e.Symbol, "261218", "261116")
		e.Expiry = "2026-11-16"
		e.ReferenceObservedAt, e.ReferenceAvailableAt = o.Chain.AsOf, o.Chain.AsOf
		e.PriceOccurredAt, e.PriceObservedAt, e.PriceAvailableAt = o.Chain.Spot.At, o.Chain.AsOf, o.Chain.AsOf
	}
	for i := range o.Structure.Legs {
		o.Structure.Legs[i].Symbol = strings.ReplaceAll(o.Structure.Legs[i].Symbol, "261218", "261116")
		o.Order.Legs[i].Symbol = o.Structure.Legs[i].Symbol
	}
	if err := o.Chain.Validate(o.Chain.AsOf, o.Chain.AssetID); err != nil {
		t.Fatal("source must precede expiry", err)
	}
	if err := admit(context.Background(), o, o.Chain.AsOf.Add(time.Hour)); err == nil {
		t.Fatal("expired contracts reached opening admission")
	}
}
