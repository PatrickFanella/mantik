package issuerdoc

// supported lists instruments whose issuer method and identity were verified
// against a retained sponsor document. Coverage requires an issuer document
// exactly for these instruments; extend only after verifying a new one.
var supported = map[string]Spec{
	"SPY": {Method: MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103"},
}

// SupportedMethod returns the verified issuer method and CUSIP for a symbol.
func SupportedMethod(symbol string) (method, cusip string, ok bool) {
	s, ok := supported[symbol]
	return s.Method, s.CUSIP, ok
}
