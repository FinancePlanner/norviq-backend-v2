package receipts

import (
	"math"
	"slices"
	"testing"
)

// Ports ReceiptLineItemExtractionTests.swift plus the fiscal QR parser
// cases: nothing a model or a QR returns can become a bogus expense line.

func draftFrom(t *testing.T, raw string) *Draft {
	t.Helper()
	e, ok := DecodeExtracted([]byte(raw))
	if !ok {
		t.Fatalf("decode %s", raw)
	}
	return e.ToDraft()
}

// reconcile is ReceiptDraft.lineItemsReconcile (shared, client-side).
func reconcile(d *Draft) *bool {
	if len(d.LineItems) == 0 || d.Total == nil {
		return nil
	}
	sum := 0.0
	for _, li := range d.LineItems {
		sum += li.Amount
	}
	ok := math.Abs(sum-*d.Total) < 0.01
	return &ok
}

func TestExtractsLineItems(t *testing.T) {
	d := draftFrom(t, `{"merchant":"Pingo Doce","total":5.50,"currency":"eur","date":"2026-09-01",
	 "lineItems":[{"description":"Pão","amount":2.10},{"description":"Leite","amount":3.40,"quantity":2}]}`)
	if len(d.LineItems) != 2 || d.LineItems[0].Description != "Pão" || *d.LineItems[1].Quantity != 2 ||
		*d.Currency != "EUR" || d.Source != SourceOCR || d.Confidence != 0.6 {
		t.Fatalf("%+v", d)
	}
}

func TestReconciles(t *testing.T) {
	d := draftFrom(t, `{"total":5.50,"lineItems":[{"description":"Pão","amount":2.10},{"description":"Leite","amount":3.40}]}`)
	if r := reconcile(d); r == nil || !*r {
		t.Fatal("should reconcile")
	}
}

func TestDoesNotReconcile(t *testing.T) {
	d := draftFrom(t, `{"total":9.90,"lineItems":[{"description":"Pão","amount":2.10}]}`)
	if r := reconcile(d); r == nil || *r {
		t.Fatal("should not reconcile")
	}
}

func TestDropsAmountlessLines(t *testing.T) {
	d := draftFrom(t, `{"total":2.10,"lineItems":[{"description":"Pão","amount":2.10},{"description":"Saco"},{"amount":1.00}]}`)
	var names []string
	for _, li := range d.LineItems {
		names = append(names, li.Description)
	}
	if !slices.Equal(names, []string{"Pão"}) {
		t.Fatalf("%v", names)
	}
}

func TestDropsBlankDescriptions(t *testing.T) {
	if d := draftFrom(t, `{"lineItems":[{"description":"   ","amount":1.0}]}`); len(d.LineItems) != 0 {
		t.Fatalf("%+v", d.LineItems)
	}
}

func TestHeaderOnly(t *testing.T) {
	d := draftFrom(t, `{"merchant":"Continente","total":12.30}`)
	if len(d.LineItems) != 0 || reconcile(d) != nil || *d.Merchant != "Continente" {
		t.Fatalf("%+v", d)
	}
}

func TestLineItemsAloneAreContent(t *testing.T) {
	e, ok := DecodeExtracted([]byte(`{"lineItems":[{"description":"Pão","amount":2.10}]}`))
	if !ok || !e.HasContent() {
		t.Fatal("should be content")
	}
}

func TestEmptyIsNotContent(t *testing.T) {
	e, ok := DecodeExtracted([]byte(`{}`))
	if !ok || e.HasContent() {
		t.Fatal("should not be content")
	}
}

// JSONDecoder is strict: a mistyped value rejects the whole object, and
// keys match exactly.
func TestExtractionIsStrict(t *testing.T) {
	for _, raw := range []string{`{"total":"5.50"}`, `{"lineItems":[{"description":1,"amount":1}]}`, `{"lineItems":[null]}`, `[]`, `not json`} {
		if _, ok := DecodeExtracted([]byte(raw)); ok {
			t.Errorf("%s should not decode", raw)
		}
	}
	if e, ok := DecodeExtracted([]byte(`{"Merchant":"x"}`)); !ok || e.Merchant != nil {
		t.Errorf("case-insensitive key matched: %+v", e)
	}
}

func TestParsesPortugalATQR(t *testing.T) {
	payload := "A:123456789*B:999999990*C:PT*D:FS*E:N*F:20260901*G:FS 1/1*H:0*I1:PT*I3:10.00*I4:0.60*I7:20.00*I8:4.60*N:5.20*O:35.20*Q:abcd*R:1234"
	d := ParseQR("  " + payload + "\n")
	if d == nil {
		t.Fatal("not recognized")
	}
	if *d.Total != 35.20 || *d.TaxTotal != 5.20 || *d.Date != "2026-09-01" || *d.TaxID != "123456789" ||
		*d.Currency != "EUR" || d.Confidence != 1 || d.Source != SourceQR || *d.RawPayload != payload || d.Merchant != nil {
		t.Fatalf("%+v", d)
	}
	if len(d.VATLines) != 2 || d.VATLines[0].Rate != "reduced" || *d.VATLines[0].Base != 10 || d.VATLines[1].Rate != "standard" || *d.VATLines[1].Amount != 4.6 {
		t.Fatalf("vat %+v", d.VATLines)
	}
}

func TestRejectsUnusableQR(t *testing.T) {
	for _, p := range []string{"", "   ", "https://example.com", "A:123456789", "A:123*B:456", "A:1*F:2026-09-01"} {
		if d := ParseQR(p); d != nil {
			t.Errorf("%q recognized: %+v", p, d)
		}
	}
	// A date alone is enough; a malformed amount is dropped, not zero.
	if d := ParseQR("A:1*F:20260102*O:abc"); d == nil || d.Total != nil || *d.Date != "2026-01-02" {
		t.Errorf("%+v", d)
	}
	if d := ParseQR("A:1*O:0x10"); d == nil || *d.Total != 16 {
		t.Errorf("hex total: %+v", d)
	}
}
