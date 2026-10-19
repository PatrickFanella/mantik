package extraction

import (
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const RevenueProfile = "reported-revenue-v1"
const RevenueSchema = "mantik-switchyard-revenue-diagnostic-v1"
const RevenueInspectionSchema = "mantik-switchyard-revenue-inspection-v1"
const RevenueContractSchema = "mantik-revenue-diagnostic-extraction-contract-v1"
const RevenuePromptVersion = "reported-revenue-quotes-v1"

// This is a diagnostic task profile, not a global taxonomy or trading signal.
const revenueInstructions = localInstructions + `
Task profile: reported-revenue-v1. Extract at most one historical issuer revenue report from the supplied excerpt. The only allowed code is "reported_revenue" and the only allowed assessment is "reported_unverified". Do not extract forecasts, estimates, profit, expenses, prices, percentages without a reported revenue amount, or trading recommendations as revenue claims. If the source has no relevant report, abstain with no_relevant_claims. If the amount, issuer or reporting period is ambiguous, abstain with ambiguous_source; if needed source context is missing, use insufficient_source. Do not choose between multiple issuers, periods or revenue measures; abstain if a single report is not clear.
Write a concise attributed assertion naming the issuer and preserving the source's amount, units and any explicit reporting period or growth qualifier. Do not infer a currency, calendar year, fiscal period, ticker or growth rate from metadata or outside knowledge. Wording may paraphrase, but no qualifier may be added or omitted to change the claim.
Use 1–4 unique exact quotes that collectively support every assertion detail. Include the title when it supplies issuer or reporting-period context; the title may be quoted separately or together with the revenue fragment when they form one exact substring. Keep whitespace and units exactly as supplied. Choose only the context needed for the assertion; do not shorten away evidence for the issuer or period. These instructions are a target policy, not proof of semantic compliance.`

func capturePromptVersion(schema string) string {
	if schema == RevenueSchema {
		return RevenuePromptVersion
	}
	return LocalPromptVersion
}

func captureInstructions(schema string) string {
	if schema == RevenueSchema {
		return revenueInstructions
	}
	return localInstructions
}

func diagnosticCaptureSchema(schema string) bool {
	return schema == SwitchyardDiagnosticSchema || schema == RevenueSchema
}

func captureContract(a news.Article, schema string) Contract {
	c := definitionFor(a)
	if schema == RevenueSchema {
		c.Schema = RevenueContractSchema
		c.MaxClaims = 1
		c.RequiredChecks = append(c.RequiredChecks, "profile_policy")
	}
	return c
}

// Only structural task policy is enforced here. It cannot establish that an
// assertion is entailed by its quotes or that its claimed revenue is factual.
func validateRevenueResponse(r Response) error {
	if r.Outcome == "abstain" {
		return nil
	}
	if len(r.Claims) != 1 || r.Claims[0].Code != "reported_revenue" || r.Claims[0].Assessment != "reported_unverified" {
		return errors.New("revenue profile requires one reported_unverified reported_revenue claim")
	}
	return nil
}
