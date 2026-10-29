#!/usr/bin/env python3
"""Writes parity/tax-reports.jsonl. Ids match parity/tax-reports-setup.sql."""
import json
import os

U = lambda n: "7a8d0000-0000-4000-8000-00000000000%d" % n
R = lambda n: "7A8D0000-0000-4000-8000-000000000E0%d" % n
free, us, de, pack, pt, empty = (U(i) for i in range(1, 7))

cases = []


def c(name, path, as_=None, method="GET", body=None, headers=None):
    d = {"name": name, "method": method, "path": path}
    if as_:
        d["as"] = as_
    if headers:
        d["headers"] = headers
    if body is not None:
        d["body"] = body
    cases.append(d)


# Filing preview.
c("preview DE pro", "/v1/tax/filing/preview?taxYear=2025", de)
c("preview DE pro other year (no profile)", "/v1/tax/filing/preview?taxYear=2024", de)
c("preview DE pack holder", "/v1/tax/filing/preview?taxYear=2025", pack)
c("preview pack holder other year", "/v1/tax/filing/preview?taxYear=2024", pack)
c("preview free", "/v1/tax/filing/preview?taxYear=2025", free)
c("preview PT not validated", "/v1/tax/filing/preview?taxYear=2025", pt)
c("preview US pro unsupported (no profile 2025)", "/v1/tax/filing/preview?taxYear=2025", us)
c("preview US pro unsupported 2026", "/v1/tax/filing/preview?taxYear=2026", us)
c("preview no profile", "/v1/tax/filing/preview?taxYear=2025", empty)
c("preview missing taxYear", "/v1/tax/filing/preview", de)
c("preview empty taxYear", "/v1/tax/filing/preview?taxYear=", de)
c("preview taxYear 1999", "/v1/tax/filing/preview?taxYear=1999", de)
c("preview taxYear 2101", "/v1/tax/filing/preview?taxYear=2101", de)
c("preview taxYear junk", "/v1/tax/filing/preview?taxYear=abc", de)
c("preview taxYear repeated", "/v1/tax/filing/preview?taxYear=1999&taxYear=2025", de)
c("preview unauthenticated", "/v1/tax/filing/preview?taxYear=2025")

# List and get.
c("list US", "/v1/tax/reports", us)
c("list free", "/v1/tax/reports", free)
c("list pack", "/v1/tax/reports", pack)
c("list empty", "/v1/tax/reports", empty)
c("list unauthenticated", "/v1/tax/reports")
for n in range(1, 7):
    c("get US e0%d" % n, "/v1/tax/reports/" + R(n), us)
c("get lowercase id", "/v1/tax/reports/" + R(1).lower(), us)
c("get other user report", "/v1/tax/reports/" + R(1), empty)
c("get free (not Pro)", "/v1/tax/reports/" + R(7), free)
c("get pack 2025", "/v1/tax/reports/" + R(8), pack)
c("get pack 2024 (no pack)", "/v1/tax/reports/" + R(9), pack)
c("get bad uuid", "/v1/tax/reports/not-a-uuid", us)
c("get unknown", "/v1/tax/reports/7A8D0000-0000-4000-8000-000000000EFF", us)

# Downloads. The fixture CSV is 101 bytes with mtime 2026-01-01T00:00:00Z.
dl = lambda n: "/v1/tax/reports/" + R(n) + "/download"
c("download csv", dl(1), us)
c("download pdf", dl(2), us)
c("download expired", dl(3), us)
c("download missing file", dl(4), us)
c("download failed", dl(5), us)
c("download expired status", dl(6), us)
c("download range", dl(1), us, headers={"Range": "bytes=0-9"})
c("download suffix range", dl(1), us, headers={"Range": "bytes=-5"})
c("download open range", dl(2), us, headers={"Range": "bytes=100-"})
c("download unsatisfiable range", dl(1), us, headers={"Range": "bytes=500-600"})
c("download multi range", dl(1), us, headers={"Range": "bytes=0-1,4-5"})
c("download if-none-match", dl(1), us, headers={"If-None-Match": '"1767225600.0-101"'})
c("download if-none-match stale", dl(1), us, headers={"If-None-Match": '"1.0-1"'})
c("download free (not Pro)", dl(7), free)
c("download pack 2025", dl(8), pack)
c("download pack 2024", dl(9), pack)
c("download other user", dl(1), empty)
c("download bad uuid", "/v1/tax/reports/nope/download", us)

# Create. Each runs on both backends; Vapor's poller generates the rows.
P = "/v1/tax/reports"
c("create free not Pro", P, free, "POST", {"taxYear": 2026, "kind": "transaction_workpaper", "format": "csv"})
c("create wrong kind", P, us, "POST", {"taxYear": 2026, "kind": "nope", "format": "csv"})
c("create wrong format", P, us, "POST", {"taxYear": 2026, "kind": "form_8949", "format": "xlsx"})
c("create missing kind", P, us, "POST", {"taxYear": 2026, "format": "csv"})
c("create missing taxYear", P, us, "POST", {"kind": "form_8949", "format": "csv"})
c("create string taxYear", P, us, "POST", {"taxYear": "2026", "kind": "form_8949", "format": "csv"})
c("create empty body", P, us, "POST", {})
c("create pack holder pack 2025", P, pack, "POST", {"taxYear": 2025, "kind": "annual_filing_pack", "format": "csv"})
c("create pack holder pack 2024", P, pack, "POST", {"taxYear": 2024, "kind": "annual_filing_pack", "format": "pdf"})
c("create pack holder non-pack kind", P, pack, "POST", {"taxYear": 2025, "kind": "transaction_workpaper", "format": "csv"})
c("create free pack kind", P, free, "POST", {"taxYear": 2025, "kind": "annual_filing_pack", "format": "csv"})
c("create US workpaper pdf", P, us, "POST", {"taxYear": 2026, "kind": "transaction_workpaper", "format": "pdf"})
c("create US 8949 csv", P, us, "POST", {"taxYear": 2026, "kind": "form_8949", "format": "csv"})
c("create DE pack pdf", P, de, "POST", {"taxYear": 2025, "kind": "annual_filing_pack", "format": "pdf"})
c("create unauthenticated", P, None, "POST", {"taxYear": 2026, "kind": "form_8949", "format": "csv"})

HEADER = """# Tax reports and filing pack: GET /v1/tax/filing/preview, GET/POST
# /v1/tax/reports, GET /v1/tax/reports/:reportId(/download). Generated by
# parity/tax-reports-gen.py. Load parity/tax-reports-setup.sql (see its header
# for the touch + psql line) before every pass. Both backends must see the
# same TAX_VALIDATED_JURISDICTIONS (the shared :8081 has none, so PT previews
# are a 422 and DE previews map). Ready reports point at parity/*.csv|pdf by
# absolute path, so no shared storage volume is needed.
#   go run ./cmd/parity -a http://127.0.0.1:8081 -b http://127.0.0.1:58113 -jwt-secret parity-secret \\
#     -cases parity/tax-reports.jsonl -ignore requestId,id,createdAt \\
#     -headers Content-Type,Content-Disposition,Content-Range,Accept-Ranges,ETag,Last-Modified
# id/createdAt only differ on the POST responses (new report rows); fixture
# ids are still compared through downloadPath. Content-Length is not compared:
# Vapor gzips, and Go's client drops the header when it decompresses.
# Known diff: "download if-none-match" (304) has no Content-Type on v2,
# because net/http strips it from every 304.
"""

out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tax-reports.jsonl")
with open(out, "w") as f:
    f.write(HEADER + "".join(json.dumps(x, ensure_ascii=False) + "\n" for x in cases))
