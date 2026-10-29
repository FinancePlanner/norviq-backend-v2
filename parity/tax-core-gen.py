"""Generates parity/tax-core.jsonl. Usage: python3 gen_taxcore.py OUT [only-prefix]"""
import json
import sys

P = '7a8c0000-0000-4000-8000-'


def u(suffix):
    return (P + suffix.rjust(12, '0')).upper()


FREE, US, PT, DE, EMPTY = (u(str(i)) for i in range(1, 6))
A_FREE, A_US, A_ROTH, A_PT, A_DE = u('111'), u('112'), u('122'), u('113'), u('114')
I_SPY, I_BND, I_MSFT, I_GALP, I_IWDA = u('201'), u('203'), u('204'), u('205'), u('207')
L_SPY, L_AAPL, L_BND, L_GALP, L_IWDA, L_QQQ = u('301'), u('302'), u('303'), u('308'), u('311'), u('313')
VTI = '423CC87B-6884-479D-9C79-4EC75C0FCB84'
SCHG = 'ED99F92D-F867-45AA-948A-BB32BD0CDD8B'
S1, S2, S3 = u('b01'), u('b02'), u('b03')  # harvest, harvest (unplanned), location
P1 = u('c01')  # action plan from S1
MISSING = '7A8C0000-0000-4000-8000-0000DEADBEEF'
LOCATION = 'location:%s:%s:%s:%s' % (I_SPY, I_MSFT, A_US, A_ROTH)


def member():
    return [{"id": "self", "displayName": "You", "relationship": "self"}]


def acct(cid, aid, w="taxable"):
    return {"id": cid, "accountId": aid, "ownerMemberId": "self", "wrapper": w, "lotSelectionMethod": "fifo"}


us_profile = {
    "jurisdiction": "US", "taxYear": 2026, "filingStatus": "single", "reportingCurrency": "USD",
    "estimatedTaxableIncome": 120000, "shortTermCapitalGainsRate": 0.32, "longTermCapitalGainsRate": 0.15,
    "priorShortTermLossCarryover": 250, "priorLongTermLossCarryover": 0,
    "members": member(), "accounts": [acct("c1", A_US), acct("c2", A_ROTH, "roth_ira")]}
pt_profile = {
    "jurisdiction": "PT", "taxYear": 2026, "filingStatus": "single", "reportingCurrency": "EUR",
    "estimatedTaxableIncome": 42000, "marginalIncomeTaxRate": 0.37, "capitalGainsTaxationMode": "autonomous",
    "priorShortTermLossCarryover": 0, "priorLongTermLossCarryover": 0,
    "members": member(), "accounts": [acct("c1", A_PT)]}
de_profile = {
    "jurisdiction": "DE", "taxYear": 2026, "filingStatus": "single", "reportingCurrency": "EUR",
    "estimatedTaxableIncome": 65000, "remainingCapitalIncomeAllowance": 500, "churchTaxRate": 0.09,
    "priorShortTermLossCarryover": 0, "priorLongTermLossCarryover": 0,
    "members": member(), "accounts": [acct("c1", A_DE)]}
free_profile = dict(us_profile, accounts=[acct("c1", A_FREE)])

fund_input = {
    "accountId": A_DE, "instrumentId": I_IWDA, "calculationYear": 2025, "currency": "EUR",
    "holdings": [{"id": "h1", "lotId": L_IWDA, "quantity": 40, "beginningMarketValue": 3200,
                  "endingMarketValue": 3800, "distributions": 12.5, "acquisitionMonth": 3}]}

cases = []


def c(name, method, path, as_, body=None):
    d = {"name": name, "method": method, "path": path, "as": as_}
    if body is not None:
        d["body"] = body
    cases.append(d)


# Profiles are saved first; every later read sees them.
c("profile save US", "PUT", "/v1/tax/profile", US, us_profile)
c("profile save PT", "PUT", "/v1/tax/profile", PT, pt_profile)
c("profile save DE", "PUT", "/v1/tax/profile", DE, de_profile)
c("profile save free 402", "PUT", "/v1/tax/profile", FREE, free_profile)
c("profile save bad year 422", "PUT", "/v1/tax/profile", US, dict(us_profile, taxYear=2019))
missing = dict(us_profile)
del missing["filingStatus"]
c("profile save missing key 400", "PUT", "/v1/tax/profile", US, missing)
c("profile save bad enum 400", "PUT", "/v1/tax/profile", US, dict(us_profile, jurisdiction="XX"))
c("profile save anonymous 401", "PUT", "/v1/tax/profile", "", us_profile)

# Capabilities.
c("capabilities", "GET", "/v1/tax/capabilities", FREE)
c("capabilities PT 2025", "GET", "/v1/tax/capabilities?jurisdiction=PT&taxYear=2025", FREE)
c("capabilities bad jurisdiction 400", "GET", "/v1/tax/capabilities?jurisdiction=XX", FREE)
c("capabilities bad year 400", "GET", "/v1/tax/capabilities?taxYear=soon", FREE)
c("capabilities anonymous 401", "GET", "/v1/tax/capabilities", "")

# Profile reads.
c("profile US", "GET", "/v1/tax/profile", US)
c("profile PT", "GET", "/v1/tax/profile?jurisdiction=PT&taxYear=2026", PT)
c("profile DE", "GET", "/v1/tax/profile?jurisdiction=DE", DE)
c("profile empty 404", "GET", "/v1/tax/profile", EMPTY)
c("profile free 402", "GET", "/v1/tax/profile", FREE)
c("profile other year 404", "GET", "/v1/tax/profile?taxYear=2025", US)
c("profile context US", "GET", "/v1/tax/profile/context", US)
c("profile context DE", "GET", "/v1/tax/profile/context?jurisdiction=DE", DE)
c("profile context PT", "GET", "/v1/tax/profile/context?jurisdiction=PT", PT)
c("profile context empty", "GET", "/v1/tax/profile/context?jurisdiction=ES", EMPTY)
c("profile context free 402", "GET", "/v1/tax/profile/context", FREE)

# Dashboards.
c("dashboard US", "GET", "/v1/tax/dashboard", US)
c("dashboard US 2026", "GET", "/v1/tax/dashboard?jurisdiction=US&taxYear=2026", US)
c("dashboard PT", "GET", "/v1/tax/dashboard?jurisdiction=PT", PT)
c("dashboard DE", "GET", "/v1/tax/dashboard?jurisdiction=DE", DE)
c("dashboard free", "GET", "/v1/tax/dashboard", FREE)
c("dashboard empty", "GET", "/v1/tax/dashboard", EMPTY)
c("dashboard FR no profile", "GET", "/v1/tax/dashboard?jurisdiction=FR", US)
c("dashboard bad year 400", "GET", "/v1/tax/dashboard?taxYear=x", US)

# Loss carryforward ledgers.
c("ledger US", "GET", "/v1/tax/loss-carryforwards", US)
c("ledger PT 2026", "GET", "/v1/tax/loss-carryforwards?jurisdiction=PT&taxYear=2026", PT)
c("ledger PT 2025", "GET", "/v1/tax/loss-carryforwards?jurisdiction=PT&taxYear=2025", PT)
c("ledger DE 2026", "GET", "/v1/tax/loss-carryforwards?jurisdiction=DE&taxYear=2026", DE)
c("ledger DE 2025", "GET", "/v1/tax/loss-carryforwards?jurisdiction=DE&taxYear=2025", DE)
c("ledger ES empty", "GET", "/v1/tax/loss-carryforwards?jurisdiction=ES", EMPTY)
c("ledger FR", "GET", "/v1/tax/loss-carryforwards?jurisdiction=FR", DE)
c("ledger free", "GET", "/v1/tax/loss-carryforwards", FREE)

# Instruments.
c("market admission IWDA", "PUT", "/v1/tax/instruments/%s/market-admission" % I_IWDA, DE, {"status": "regulated"})
c("market admission unknown 404", "PUT", "/v1/tax/instruments/%s/market-admission" % MISSING, DE, {"status": "regulated"})
c("market admission bad enum 400", "PUT", "/v1/tax/instruments/%s/market-admission" % I_IWDA, DE, {"status": "maybe"})
c("market admission bad id 400", "PUT", "/v1/tax/instruments/nope/market-admission", DE, {"status": "regulated"})
c("market admission free 402", "PUT", "/v1/tax/instruments/%s/market-admission" % I_IWDA, FREE, {"status": "regulated"})
c("fund classification IWDA", "PUT", "/v1/tax/instruments/%s/fund-classification" % I_IWDA, DE, {"classification": "equity"})
c("fund classification other user 404", "PUT", "/v1/tax/instruments/%s/fund-classification" % I_IWDA, PT, {"classification": "equity"})
c("fund classification bad enum 400", "PUT", "/v1/tax/instruments/%s/fund-classification" % I_IWDA, DE, {"classification": "bonds"})

# German fund annual inputs.
c("fund input save", "PUT", "/v1/tax/funds/annual-inputs", DE, fund_input)
c("fund input save bad year 422", "PUT", "/v1/tax/funds/annual-inputs", DE, dict(fund_input, calculationYear=2024))
c("fund input save bad month 422", "PUT", "/v1/tax/funds/annual-inputs", DE,
  dict(fund_input, holdings=[dict(fund_input["holdings"][0], acquisitionMonth=13)]))
c("fund input save foreign account 404", "PUT", "/v1/tax/funds/annual-inputs", PT, fund_input)
fq = "/v1/tax/funds/annual-inputs?accountId=%s&instrumentId=%s&calculationYear=%s"
c("fund input get", "GET", fq % (A_DE, I_IWDA, 2025), DE)
c("fund input get none 404", "GET", fq % (A_DE, I_IWDA, 2026), DE)
c("fund input get missing year 400", "GET", "/v1/tax/funds/annual-inputs?accountId=%s&instrumentId=%s" % (A_DE, I_IWDA), DE)
c("fund input get bad uuid 400", "GET", fq % ("nope", I_IWDA, 2025), DE)

# Scenarios.
scenario = {"taxYear": 2026, "opportunityIds": [L_SPY], "plannedReplacementInstrumentIds": {L_SPY: VTI}}
c("scenario create", "POST", "/v1/tax/scenarios", US, scenario)
c("scenario create two", "POST", "/v1/tax/scenarios", US,
  {"taxYear": 2026, "opportunityIds": [L_SPY, L_QQQ], "plannedReplacementInstrumentIds": {L_SPY: VTI, L_QQQ: SCHG}})
c("scenario create without replacement 422", "POST", "/v1/tax/scenarios", US,
  {"taxYear": 2026, "opportunityIds": [L_SPY, L_QQQ], "plannedReplacementInstrumentIds": {}})
c("scenario create wrong replacement 422", "POST", "/v1/tax/scenarios", US,
  {"taxYear": 2026, "opportunityIds": [L_QQQ], "plannedReplacementInstrumentIds": {L_QQQ: VTI}})
c("scenario create unknown opportunity 422", "POST", "/v1/tax/scenarios", US,
  {"taxYear": 2026, "opportunityIds": ["nope"], "plannedReplacementInstrumentIds": {}})
c("scenario create empty 422", "POST", "/v1/tax/scenarios", US,
  {"taxYear": 2026, "opportunityIds": [], "plannedReplacementInstrumentIds": {}})
c("scenario create no profile", "POST", "/v1/tax/scenarios", EMPTY, scenario)
c("scenario create free 402", "POST", "/v1/tax/scenarios", FREE, scenario)
c("scenario get", "GET", "/v1/tax/scenarios/%s" % S1, US)
c("scenario get other user 404", "GET", "/v1/tax/scenarios/%s" % S1, PT)
c("scenario get bad id 400", "GET", "/v1/tax/scenarios/nope", US)

# Location scenarios.
c("location scenario create", "POST", "/v1/tax/location-scenarios", US, {"taxYear": 2026, "opportunityIds": [LOCATION]})
c("location scenario unknown 422", "POST", "/v1/tax/location-scenarios", US, {"taxYear": 2026, "opportunityIds": ["location:nope"]})

# Action plans: P1 (from S1) is seeded; S2 and S3 have none yet.
c("action plans list", "GET", "/v1/tax/action-plans", US)
c("action plans list empty", "GET", "/v1/tax/action-plans", EMPTY)
c("action plan get", "GET", "/v1/tax/action-plans/%s" % P1, US)
c("action plan get 404", "GET", "/v1/tax/action-plans/%s" % MISSING, US)
c("action plan create", "POST", "/v1/tax/action-plans", US, {"scenarioId": S2, "idempotencyKey": "parity-tax-core-plan-s2"})
c("action plan create unknown scenario 404", "POST", "/v1/tax/action-plans", US, {"scenarioId": MISSING, "idempotencyKey": "parity-tax-core-plan-missing"})
c("action plan create location scenario 422", "POST", "/v1/tax/action-plans", US, {"scenarioId": S3, "idempotencyKey": "parity-tax-core-plan-s3"})
c("placement plan create", "POST", "/v1/tax/placement-plans", US, {"scenarioId": S3, "idempotencyKey": "parity-tax-core-placement-s3"})
c("placement plan harvest scenario 422", "POST", "/v1/tax/placement-plans", US, {"scenarioId": S1, "idempotencyKey": "parity-tax-core-placement-s1"})
c("action plan transition bad status 422", "PATCH", "/v1/tax/action-plans/%s" % P1, US, {"status": "accepted"})
c("action plan transition 404", "PATCH", "/v1/tax/action-plans/%s" % MISSING, US, {"status": "cancelled"})
c("action plan cancel", "PATCH", "/v1/tax/action-plans/%s" % P1, US, {"status": "cancelled", "confirmationNote": "Changed my mind"})
c("action plan complete after cancel 409", "PATCH", "/v1/tax/action-plans/%s" % P1, US, {"status": "completed"})
c("action plans list after", "GET", "/v1/tax/action-plans", US)

# Opportunity decisions.
c("dismiss QQQ", "POST", "/v1/tax/opportunities/%s/dismiss" % L_QQQ, US)
c("dismiss not an opportunity 404", "POST", "/v1/tax/opportunities/%s/dismiss" % L_BND, US)
c("dismiss unknown 404", "POST", "/v1/tax/opportunities/nope/dismiss", US)
c("dashboard after dismiss", "GET", "/v1/tax/dashboard", US)
c("restore QQQ", "DELETE", "/v1/tax/opportunities/%s/dismiss" % L_QQQ, US)
c("restore again", "DELETE", "/v1/tax/opportunities/%s/dismiss" % L_QQQ, US)
c("restore planned 409", "DELETE", "/v1/tax/opportunities/parity-planned-opportunity/dismiss", US)
c("restore free 402", "DELETE", "/v1/tax/opportunities/%s/dismiss" % L_BND, FREE)

# Notifications.
c("notifications default", "GET", "/v1/tax/notifications", EMPTY)
c("notifications save", "PUT", "/v1/tax/notifications", US, {"enabled": True, "minimumBenefit": 150.5, "cooldownDays": 0})
c("notifications get", "GET", "/v1/tax/notifications", US)
c("notifications save bad body 400", "PUT", "/v1/tax/notifications", US, {"enabled": "yes", "cooldownDays": 3})
c("notifications free", "GET", "/v1/tax/notifications", FREE)

only = sys.argv[2] if len(sys.argv) > 2 else ""
out = [x for x in cases if x["name"].startswith(only)] if only else cases
header = """# Tax core routes (TaxController minus reports). Load parity/tax-core-setup.sql
# first, then run against a Vapor with TAX_VALIDATED_JURISDICTIONS=US,PT (and
# v2 with the same), forwards and backwards; see the setup file for why.
#   go run ./cmd/parity -a http://127.0.0.1:48120 -b http://127.0.0.1:48113 -jwt-secret parity-secret \\
#     -cases parity/tax-core.jsonl -ignore requestId,id,createdAt,updatedAt,generatedAt
"""
with open(sys.argv[1], "w") as f:
    if not only:
        f.write(header)
    f.write("\n".join(json.dumps(x) for x in out) + "\n")
